package main

import (
	"encoding/json"
	"flag"
	"html/template"
	"log"
	"net/http"
	"sync/atomic"

	"fake_dowloader/util"
)

var (
	stopChan     chan struct{}
	currentSpeed int64
)

func main() {
	// 解析命令行参数
	var port string
	flag.StringVar(&port, "port", "8084", "Port to listen on")
	flag.Parse()

	// 解析模板
	tmpl, err := template.ParseFiles("templates/index.html")
	if err != nil {
		log.Fatalf("Failed to parse template: %v", err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			err := tmpl.Execute(w, nil)
			if err != nil {
				http.Error(w, "Failed to render template", http.StatusInternalServerError)
			}
		}
	})

	http.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
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

	// 打印用户输入的端口号
	log.Printf("start to listen port: %s...", port)

	log.Fatal(http.ListenAndServe(":"+port, nil))
}
