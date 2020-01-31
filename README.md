# Video-Streamer-Encoder

Stream videos using HTTP/1.1 `chunked` Transfer Encoding while simultaneously
encoding and saving to disk for future video plays.

## Background

I have a huge collection of HD home videos that I wish to share with family and
friends. A wide variety of (low-end and high-end of different form-factors) devices 
will be used to access these and I didn't want to waste bandwidth streaming
the full resolution. At the same time, I didn't want to spend days transcoding
the videos when only a few select videos may be viewed. So I wanted something
dynamic and came up with this solution.

## Uses

* [Golang](https://golang.org/) is the programming language used to build the server
* [FFmpeg](https://ffmpeg.org/) for video streaming and encoding

## Install

Build the binary to run the server:

```
$ cd git clone https://github.com/theju/video-streamer-encoder.git
$ cd video-streamer-encoder
$ go build server.go
```

## Configuration

The `configuration` file looks like this:

```
{
    "Host": "localhost",
    "Port": 8000,
    "InputDir": "/path/to/directory/containing/videos",
    "OutputDir": "/path/where/encoded/files/are/stored",
    "Widths": [240, 480, 720, 1080]
}
```

All the attributes except `Widths` are self-explanatory. `Widths` takes an array
of resolutions to which the videos are encoded.

## Usage

Run the server

```
./server --config=/path/to/config.json
```

In your browser, access the URL of the video 

```
http://localhost:8000/480p/video_filename.mp4
```

The above step assumes that `video_filename.mp4` exists in the `InputDir`
and will be encoded to 480p resolution and saved in a sub-directory `480` 
under the `OutputDir`.

## TODO

* Make use of FFmpeg API
* Send the most optimal amount of data (currently 64K per chunk) based on connection speed
* Evaluate Http2 streaming

## License

MIT License. Please refer the `LICENSE` file.
