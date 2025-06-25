package main

import (
	"embed" // 引入 embed 包
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"fake_dowloader/util"
)

// --- 嵌入资源 ---
// 使用编译器指令嵌入整个 templates 目录和 py 脚本。
// 这些资源将在“生产模式”下使用。
//go:embed templates/*
var templateFS embed.FS

//go:embed py/force_reannounce.py
var pythonScript []byte

// --- 全局变量 ---
var (
	// 定义一个全局的模板变量
	templates *template.Template
	// 定义一个开发模式的标志
	devMode bool
)

type Session struct {
	ID        string
	StopChan  chan struct{}
	Speed     *int64
	CreatedAt time.Time
}

type SessionManager struct {
	sessions map[string]*Session
	mutex    sync.RWMutex
}

func NewSessionManager() *SessionManager {
	sm := &SessionManager{
		sessions: make(map[string]*Session),
	}
	go sm.cleanupExpiredSessions()
	return sm
}

func (sm *SessionManager) CreateSession(sessionID string) *Session {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	if existingSession, exists := sm.sessions[sessionID]; exists {
		if existingSession.StopChan != nil {
			close(existingSession.StopChan)
		}
	}
	session := &Session{
		ID:        sessionID,
		StopChan:  make(chan struct{}),
		Speed:     new(int64),
		CreatedAt: time.Now(),
	}
	sm.sessions[sessionID] = session
	return session
}

func (sm *SessionManager) GetSession(sessionID string) (*Session, bool) {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()
	session, exists := sm.sessions[sessionID]
	return session, exists
}

func (sm *SessionManager) StopSession(sessionID string) bool {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	session, exists := sm.sessions[sessionID]
	if !exists {
		return false
	}
	if session.StopChan != nil {
		close(session.StopChan)
	}
	delete(sm.sessions, sessionID)
	return true
}

func (sm *SessionManager) cleanupExpiredSessions() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		sm.mutex.Lock()
		now := time.Now()
		for sessionID, session := range sm.sessions {
			if now.Sub(session.CreatedAt) > 30*time.Minute {
				if session.StopChan != nil {
					close(session.StopChan)
				}
				delete(sm.sessions, sessionID)
				log.Printf("Cleaned up expired session: %s", sessionID)
			}
		}
		sm.mutex.Unlock()
	}
}

var sessionManager = NewSessionManager()

func getSessionID(r *http.Request) string {
	return r.RemoteAddr + "_" + r.UserAgent()
}

func handleReannounce(w http.ResponseWriter, r *http.Request) {
	defer recoverPanic(w)

	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	var cmd *exec.Cmd
	// 根据是否为开发模式，选择不同的方式执行 Python 脚本
	if devMode {
		// 开发模式：直接从文件系统读取脚本路径
		log.Println("Dev mode: running script from filesystem.")
		cwd, err := os.Getwd()
		if err != nil {
			http.Error(w, "Failed to get current working directory: "+err.Error(), http.StatusInternalServerError)
			return
		}
		scriptPath := filepath.Join(cwd, "py", "force_reannounce.py")
		cmd = exec.Command("python3", scriptPath)
	} else {
		// 生产模式：将嵌入的脚本写入临时文件再执行
		log.Println("Production mode: running embedded script.")
		tmpfile, err := os.CreateTemp("", "force_reannounce_*.py")
		if err != nil {
			http.Error(w, "Failed to create temporary script file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer os.Remove(tmpfile.Name())

		if _, err := tmpfile.Write(pythonScript); err != nil {
			http.Error(w, "Failed to write script to temporary file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := tmpfile.Close(); err != nil {
			http.Error(w, "Failed to close temporary script file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		os.Chmod(tmpfile.Name(), 0700)
		cmd = exec.Command("python3", tmpfile.Name())
	}

	log.Println("Executing command:", cmd.String())
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Command failed with error: %v\nOutput:\n%s", err, string(output))
		http.Error(w, "Command failed: "+err.Error()+"\nOutput: "+string(output), http.StatusInternalServerError)
		return
	}

	log.Println("Script output:\n", string(output))
	w.Write([]byte("Re-announce completed with output:\n" + string(output)))
}

func recoverPanic(w http.ResponseWriter) {
	if r := recover(); r != nil {
		log.Println("Recovered from panic:", r)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func main() {
	var port string
	var defaultPeerAddr string
	// 添加一个 -dev 标志，用于切换到开发模式
	flag.BoolVar(&devMode, "dev", false, "Enable development mode to load files from disk")
	flag.StringVar(&port, "port", "5000", "Port to listen on")
	flag.StringVar(&defaultPeerAddr, "addr", "127.0.0.1:63219", "Default peer address for the web UI input")
	flag.Parse()

	var err error
	// 根据是否为开发模式，选择不同的方式加载模板
	if devMode {
		log.Println("Running in DEVELOPMENT mode. Loading templates from filesystem.")
		templates, err = template.ParseFiles("templates/index.html")
	} else {
		log.Println("Running in PRODUCTION mode. Loading templates from embedded assets.")
		templates, err = template.ParseFS(templateFS, "templates/index.html")
	}

	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method == http.MethodGet {
			data := struct {
				DefaultPeerAddr string
			}{
				DefaultPeerAddr: defaultPeerAddr,
			}
			// 使用全局的 templates 变量来执行
			err := templates.Execute(w, data)
			if err != nil {
				http.Error(w, "Failed to render template", http.StatusInternalServerError)
			}
		}
	})

	http.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method == http.MethodPost {
			sessionID := getSessionID(r)
			peerAddr := r.FormValue("peerAddr")
			infoHash := r.FormValue("infoHash")
			session := sessionManager.CreateSession(sessionID)
			go func() {
				err := util.ConnectPeerWithStop(peerAddr, infoHash, session.StopChan, session.Speed)
				if err != nil {
					log.Printf("Session %s connection error: %v", sessionID, err)
				}
			}()
			log.Printf("Started session: %s", sessionID)
			w.Write([]byte("Started"))
		} else {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method == http.MethodPost {
			sessionID := getSessionID(r)
			if sessionManager.StopSession(sessionID) {
				log.Printf("Stopped session: %s", sessionID)
				w.Write([]byte("Stopped"))
			} else {
				w.Write([]byte("No active session found"))
			}
		} else {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/speed", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method == http.MethodGet {
			sessionID := getSessionID(r)
			session, exists := sessionManager.GetSession(sessionID)
			var speed int64 = 0
			if exists {
				speed = atomic.LoadInt64(session.Speed)
			}
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

	http.HandleFunc("/test-concurrent", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		sessionCount := len(sessionManager.sessions)
		response := fmt.Sprintf("Active sessions: %d\nSession IDs:\n", sessionCount)
		sessionManager.mutex.RLock()
		for sessionID := range sessionManager.sessions {
			response += fmt.Sprintf("- %s\n", sessionID)
		}
		sessionManager.mutex.RUnlock()
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(response))
	})

	log.Printf("Starting server on port: %s...", port)
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, nil))
}

