package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
)

type JSONConfig struct {
	Host string
	Port int
	InputDir string
	OutputDir string
	Widths []int
}

func main() {
	var configFile string
	flag.StringVar(&configFile, "config", "config.json", "JSON Config file")
	data, configFileErr := ioutil.ReadFile(configFile)
	if configFileErr != nil {
		log.Fatal("Config file not found")
	}
	var config JSONConfig
	unmarshalErr := json.Unmarshal(data, &config)
	if unmarshalErr != nil {
		log.Fatal("Invalid Config file")
	}
	urlRegex, regexpErr := regexp.Compile("/(?P<width>\\d+)p/(?P<filename>.*?)$")
	if regexpErr != nil {
		log.Fatal("Invalid regexp")
	}
	http.HandleFunc("/", func(rw http.ResponseWriter, req *http.Request) {
		reqPath := req.URL.Path
		matches := urlRegex.MatchString(reqPath)
		flusher, ok := rw.(http.Flusher)
		if ok != true {
			rw.WriteHeader(http.StatusBadRequest)
			rw.Write([]byte("Invalid Flusher"))
		}
		if matches == true {
			ret := urlRegex.FindStringSubmatch(reqPath)
			width, widthConvErr := strconv.Atoi(ret[1])
			if widthConvErr != nil {
				rw.WriteHeader(http.StatusBadRequest)
				rw.Write([]byte("Invalid Width"))
				return
			}
			found := false
			for _, ii := range config.Widths {
				if ii == width {
					found = true
				}
			}
			if (found == false) {
				rw.WriteHeader(http.StatusBadRequest)
				rw.Write([]byte("Invalid Width"))
				return
			}
			outputDir := fmt.Sprintf("%s/%d", config.OutputDir, width)
			dirErr := os.MkdirAll(outputDir, os.ModePerm)
			if (dirErr != nil) {
				rw.WriteHeader(http.StatusBadRequest)
				rw.Write([]byte("Could not create temporary directory"))
				return
			}
			filename := ret[2]
			origFile, origFileErr := os.Open(fmt.Sprintf("%s/%s", config.InputDir, filename))
			defer origFile.Close()
			if origFileErr != nil {
				rw.WriteHeader(http.StatusNotFound)
				rw.Write([]byte("Not Found"))
				return
			} else {
				trFileName := fmt.Sprintf("%s/%s", outputDir, filename)
				_, trFileErr := os.Stat(trFileName)
				if trFileErr == nil {
					http.ServeFile(rw, req, trFileName)
				} else {
					tempFile, tempFileErr := ioutil.TempFile(
						outputDir,
						path.Base(origFile.Name()))
					defer tempFile.Close()
					if tempFileErr != nil {
						rw.WriteHeader(http.StatusBadRequest)
						rw.Write([]byte("Could not create temporary file"))
						return
					}
					ctx := req.Context()
					rw.Header().Set("Transfer-Encoding", "chunked")
					tret := transcodeFile(origFile.Name(), width, tempFile.Name())
					cmd := tret.cmd
					defer cmd.Process.Kill()
					defer cmd.Process.Wait()
					rc := *(tret.rc)
					defer rc.Close()
					done := 0
					for {
						_, err := io.CopyN(rw, rc, 16*1024)
						if err != nil {
							done = 1
							if err == io.EOF {
								os.Rename(tempFile.Name(), trFileName)
								break
							}
							break
						}
						select {
						case <- ctx.Done():
							done = 1
							break
						default:
							break
						}
						if done == 1 {
							os.Remove(tempFile.Name())
							break
						}
						flusher.Flush()
					}
				}
			}
		} else {
			rw.WriteHeader(http.StatusNotFound)
			rw.Write([]byte("Not Found"))
			return
		}
	})

	http.ListenAndServe(fmt.Sprintf("%s:%d", config.Host, config.Port), nil)
}

type TranscodeRet struct {
	cmd *exec.Cmd
	rc *io.ReadCloser
}

func transcodeFile(inputFile string, width int, outputFile string) TranscodeRet {
	cmd := exec.Command(
		"ffmpeg", "-y", "-i", inputFile,
		"-filter_complex", fmt.Sprintf("scale=%d:-2[mid];[mid]split=2[out1][out2]", width),
		"-map", "0:a", "-c:a", "copy",
		"-map", "[out1]", "-f", "mp4", outputFile,
		"-map", "0:a", "-c:a", "copy",
		"-map", "[out2]", "-movflags", "isml+frag_keyframe", "-f", "ismv", "-",
	)
	reader, readerErr := cmd.StdoutPipe()
	if readerErr != nil {
		fmt.Printf("Error %s\n", readerErr.Error())
	}
	err := cmd.Start()
	if err != nil {
		fmt.Printf("Error %s\n", err.Error())
	}
	var tr TranscodeRet
	tr.cmd = cmd
	tr.rc = &reader
	return tr
}
