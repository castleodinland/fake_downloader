package main

import (
	"encoding/json"
	"flag"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"

	"fake_dowloader/util"
)

var (
	stopChan     chan struct{}
	currentSpeed int64
)

func handleReannounce(w http.ResponseWriter, r *http.Request) {
	defer recoverPanic(w)

	if r.Method == http.MethodPost {
		// 获取当前可执行文件的路径
		cwd, err := os.Getwd()
		if err != nil {
			http.Error(w, "Failed to get current working directory: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Println("Current working directory:", cwd)

		// 获取脚本的相对路径
		scriptPath := filepath.Join(cwd, "py", "force_reannounce.py")
		cmd := exec.Command("python3", scriptPath)
		log.Println("Executable path:", scriptPath)

		log.Println("Executing command:", cmd.String())
		// 获取命令的标准输出和标准错误
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			http.Error(w, "Failed to get stdout pipe: "+err.Error(), http.StatusInternalServerError)
			return
		}

		stderr, err := cmd.StderrPipe()
		if err != nil {
			http.Error(w, "Failed to get stderr pipe: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if err := cmd.Start(); err != nil {
			http.Error(w, "Failed to start command: "+err.Error(), http.StatusInternalServerError)
			return
		}

		output, _ := io.ReadAll(stdout)
		errorOutput, _ := io.ReadAll(stderr)

		if err := cmd.Wait(); err != nil {
			http.Error(w, "Command failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// 打印输出到日志中
		log.Println("Script output:\n", string(output))

		w.Write([]byte("Re-announce completed with output:\n" + string(output) + "\n" + string(errorOutput)))
	} else {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
	}
}

func recoverPanic(w http.ResponseWriter) {
	if r := recover(); r != nil {
		log.Println("Recovered from panic:", r)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func main() {
	var port string
	flag.StringVar(&port, "port", "8084", "Port to listen on")
	flag.Parse()

	tmpl, err := template.ParseFiles("templates/index.html")
	if err != nil {
		log.Fatalf("Failed to parse template: %v", err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method == http.MethodGet {
			err := tmpl.Execute(w, nil)
			if err != nil {
				http.Error(w, "Failed to render template", http.StatusInternalServerError)
			}
		}
	})

	http.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method == http.MethodPost {
			peerAddr := r.FormValue("peerAddr")
			infoHash := r.FormValue("infoHash")
			stopChan = make(chan struct{})

			go func() {
				util.ConnectPeerWithStop(peerAddr, infoHash, stopChan, &currentSpeed)
			}()

			w.Write([]byte("Started"))
		} else {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method == http.MethodPost {
			if stopChan != nil {
				close(stopChan)
			}
			w.Write([]byte("Stopped"))
		} else {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/speed", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method == http.MethodGet {
			speed := atomic.LoadInt64(&currentSpeed)
			response := map[string]int64{"speed": speed}
			jsonResponse, err := json.Marshal(response)
			if err != nil {
				http.Error(w, "Failed to create JSON response", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(jsonResponse)
		} else {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/reannounce", handleReannounce)

	log.Printf("start to listen port: %s...", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
