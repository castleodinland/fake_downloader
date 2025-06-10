package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
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

	// 启动清理goroutine，定期清理过期会话
	go sm.cleanupExpiredSessions()

	return sm
}

func (sm *SessionManager) CreateSession(sessionID string) *Session {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// 如果会话已存在，先清理
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
			// 清理超过30分钟的会话
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
	// 从请求中获取会话ID，这里使用简单的IP+UserAgent组合
	// 在生产环境中，应该使用更安全的会话管理方式
	return r.RemoteAddr + "_" + r.UserAgent()
}

func handleReannounce(w http.ResponseWriter, r *http.Request) {
	defer recoverPanic(w)

	if r.Method == http.MethodPost {
		cwd, err := os.Getwd()
		if err != nil {
			http.Error(w, "Failed to get current working directory: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Println("Current working directory:", cwd)

		scriptPath := filepath.Join(cwd, "py", "force_reannounce.py")
		cmd := exec.Command("python3", scriptPath)
		log.Println("Executable path:", scriptPath)

		log.Println("Executing command:", cmd.String())
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
	flag.StringVar(&port, "port", "5000", "Port to listen on")
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

	// 添加并发测试端点
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

	log.Printf("start to listen port: %s...", port)
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, nil))
}
