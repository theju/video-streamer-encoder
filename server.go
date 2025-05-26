package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type JSONConfig struct {
	Host      string `json:"Host"`
	Port      int    `json:"Port"`
	InputDir  string `json:"InputDir"`
	OutputDir string `json:"OutputDir"`
	Widths    []int  `json:"Widths"`
}

func main() {
	var configFile string
	flag.StringVar(&configFile, "config", "config.json", "JSON Config file")
	flag.Parse()

	configData, err := os.ReadFile(configFile)
	if err != nil {
		log.Fatalf("Config file not found or unreadable: %v", err)
	}

	var config JSONConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		log.Fatalf("Invalid Config file: %v", err)
	}

	urlRegex, err := regexp.Compile(`^/(\d+)p/(.+)$`)
	if err != nil {
		log.Fatalf("Invalid regexp: %v", err)
	}

	server := &http.Server{
		Addr: fmt.Sprintf("%s:%d", config.Host, config.Port),
	}

	http.HandleFunc("/", func(rw http.ResponseWriter, req *http.Request) {
		handleTranscodeRequest(rw, req, urlRegex, &config)
	})

	// Graceful shutdown setup
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("Server starting on %s:%d...", config.Host, config.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("ListenAndServe error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down server...")
	ctxShutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctxShutdown); err != nil {
		log.Fatalf("Server Shutdown Failed:%+v", err)
	}
	log.Println("Server gracefully stopped.")
}

func handleTranscodeRequest(rw http.ResponseWriter, req *http.Request, urlRegex *regexp.Regexp, config *JSONConfig) {
	reqPath := req.URL.Path

	matches := urlRegex.FindStringSubmatch(reqPath)
	if len(matches) != 3 {
		httpError(rw, http.StatusNotFound, "Not Found")
		return
	}

	widthStr := matches[1]
	filename := matches[2]

	// Validate width (should be in allowed config.Widths)
	width, err := strconv.Atoi(widthStr)
	if err != nil {
		httpError(rw, http.StatusBadRequest, "Invalid Width")
		return
	}
	widthFound := false
	for _, val := range config.Widths {
		if val == width {
			widthFound = true
			break
		}
	}
	if !widthFound {
		httpError(rw, http.StatusBadRequest, "Invalid Width")
		return
	}

	// Sanitize filename to prevent path traversal
	filename = filepath.Clean(filename)
	if strings.Contains(filename, "../") || path.IsAbs(filename) {
		httpError(rw, http.StatusBadRequest, "Invalid file path")
		return
	}

	origFilePath := filepath.Join(config.InputDir, filename)
	origFile, err := os.Open(origFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			httpError(rw, http.StatusNotFound, "File Not Found")
		} else {
			log.Printf("Error opening original file: %v", err)
			httpError(rw, http.StatusInternalServerError, "Internal Server Error")
		}
		return
	}
	defer func() {
		if err := origFile.Close(); err != nil {
			log.Printf("Error closing original file: %v", err)
		}
	}()

	outputDir := filepath.Join(config.OutputDir, strconv.Itoa(width))
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		log.Printf("Error creating output directory %q: %v", outputDir, err)
		httpError(rw, http.StatusInternalServerError, "Could not create output directory")
		return
	}
	transcodedFilePath := filepath.Join(outputDir, filename)

	// If transcoded file exists, serve directly
	_, err = os.Stat(transcodedFilePath)
	if err == nil {
		http.ServeFile(rw, req, transcodedFilePath)
		return
	} else if err != nil && !os.IsNotExist(err) {
		// Other errors checking the file
		log.Printf("Error stat transcoded file: %v", err)
		httpError(rw, http.StatusInternalServerError, "Internal Server Error")
		return
	}

	flusher, ok := rw.(http.Flusher)
	if !ok {
		httpError(rw, http.StatusInternalServerError, "Streaming not supported")
		return
	}

	// Setup HTTP headers for streaming
	rw.Header().Set("Content-Type", "video/mp4")
	rw.Header().Set("Transfer-Encoding", "chunked")
	rw.WriteHeader(http.StatusOK)

	ctx := req.Context()

	trRet, err := transcodeFile(ctx, origFilePath, width, transcodedFilePath)
	if err != nil {
		log.Printf("Error starting transcoding: %v", err)
		httpError(rw, http.StatusInternalServerError, "Failed to start transcoding")
		return
	}
	defer func() {
		// Ensure process is killed in all cases
		if trRet.cmd.Process != nil {
			_ = trRet.cmd.Process.Kill()
		}
		_ = trRet.cmd.Wait()
	}()

	reader := trRet.rc
	defer reader.Close()

	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)

		buf := make([]byte, 16*1024)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				n, err := reader.Read(buf)
				if n > 0 {
					if _, errW := rw.Write(buf[:n]); errW != nil {
						log.Printf("Error writing to client: %v", errW)
						return
					}
					flusher.Flush()
				}
				if err != nil {
					if err != io.EOF {
						os.Remove(transcodedFilePath)
						log.Printf("Error reading transcoded data: %v", err)
					}
					return
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
		log.Println("Client disconnected or request cancelled. Cleaning up...")
		return
	case <-doneCh:
	}
}

func transcodeFile(ctx context.Context, inputFile string, width int, outputFile string) (*TranscodeRet, error) {
	// ffmpeg command with filters and multiple outputs
	cmd := exec.CommandContext(ctx,
		"ffmpeg", "-y", "-i", inputFile,
		"-filter_complex", fmt.Sprintf("scale=%d:-2[mid];[mid]split=2[out1][out2]", width),
		"-map", "0:a", "-c:a", "aac", "-b:a", "128k",
		"-map", "[out1]", "-c:v", "libx265", "-b:v", "1000k", "-movflags", "+faststart", outputFile,
		"-map", "0:a", "-c:a", "aac", "-b:a", "128k",
		"-map", "[out2]", "-c:v", "libx265", "-b:v", "1000k", "-movflags", "isml+frag_keyframe", "-f", "ismv", "-",
	)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("error getting stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr // For debugging ffmpeg errors

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("error starting ffmpeg: %w", err)
	}

	return &TranscodeRet{
		cmd: cmd,
		rc:  stdoutPipe,
	}, nil
}

type TranscodeRet struct {
	cmd *exec.Cmd
	rc  io.ReadCloser
}

func httpError(w http.ResponseWriter, statusCode int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte(msg))
}
