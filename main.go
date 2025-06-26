package main

import (
	"crypto/rand"
	"embed" // 引入 embed 包
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fake_dowloader/util"
)

// --- 嵌入资源 ---
//go:embed templates/*
var templateFS embed.FS

//go:embed py/force_reannounce.py
var pythonScript []byte

// --- 全局变量 ---
var (
	templates *template.Template
	devMode   bool
)

// --- Session Management ---
// Session represents a user's connection session.
type Session struct {
	ID        string
	StopChan  chan struct{}
	Speed     *int64
	CreatedAt time.Time
}

// SessionManager manages active sessions.
type SessionManager struct {
	sessions map[string]*Session
	mutex    sync.RWMutex
}

// NewSessionManager creates a new session manager.
func NewSessionManager() *SessionManager {
	sm := &SessionManager{
		sessions: make(map[string]*Session),
	}
	go sm.cleanupExpiredSessions()
	return sm
}

// CreateSession creates and stores a new session.
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

// GetSession retrieves a session by ID.
func (sm *SessionManager) GetSession(sessionID string) (*Session, bool) {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()
	session, exists := sm.sessions[sessionID]
	return session, exists
}

// StopSession stops a running session.
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

// cleanupExpiredSessions periodically removes old sessions.
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

// --- Entry Management for Saved Data ---
// SavedEntry represents a saved entry in our JSON database.
type SavedEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	PeerAddr string `json:"peerAddr"`
	InfoHash string `json:"infoHash"`
}

// EntryStore manages the collection of saved entries.
type EntryStore struct {
	mu      sync.RWMutex
	entries map[string]SavedEntry
	file    string
}

// NewEntryStore creates and loads an entry store from a file.
func NewEntryStore(file string) (*EntryStore, error) {
	store := &EntryStore{
		entries: make(map[string]SavedEntry),
		file:    file,
	}
	if err := store.load(); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		// File doesn't exist, which is fine on first start.
		// Create an empty file.
		if err := store.save(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// load reads the database file into memory.
func (s *EntryStore) load() error {
	data, err := os.ReadFile(s.file)
	if err != nil {
		return err
	}
	// If the file is empty, don't try to unmarshal
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, &s.entries)
}

// save writes the current entries from memory to the database file.
func (s *EntryStore) save() error {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.file, data, 0644)
}

// GetAll returns a slice of all saved entries.
func (s *EntryStore) GetAll() []SavedEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var entries []SavedEntry
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	return entries
}

// Add creates a new entry and saves it.
func (s *EntryStore) Add(entry SavedEntry) (SavedEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate a unique ID
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return SavedEntry{}, err
	}
	entry.ID = hex.EncodeToString(b)
	s.entries[entry.ID] = entry

	if err := s.save(); err != nil {
		// Revert change if save fails
		delete(s.entries, entry.ID)
		return SavedEntry{}, err
	}
	return entry, nil
}

// Update modifies an existing entry.
func (s *EntryStore) Update(id string, updatedEntry SavedEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	originalEntry, exists := s.entries[id]
	if !exists {
		return fmt.Errorf("entry with ID %s not found", id)
	}
	// Ensure the ID is not changed
	updatedEntry.ID = id
	s.entries[id] = updatedEntry

	if err := s.save(); err != nil {
		// Revert change if save fails
		s.entries[id] = originalEntry
		return err
	}
	return nil
}

// Delete removes an entry.
func (s *EntryStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	originalEntry, exists := s.entries[id]
	if !exists {
		return fmt.Errorf("entry with ID %s not found", id)
	}
	delete(s.entries, id)

	if err := s.save(); err != nil {
		// Revert change if save fails
		s.entries[id] = originalEntry
		return err
	}
	return nil
}

// --- Global Instances ---
var sessionManager = NewSessionManager()
var entryStore *EntryStore

// --- Utility Functions ---
func getSessionID(r *http.Request) string {
	return r.RemoteAddr + "_" + r.UserAgent()
}

func recoverPanic(w http.ResponseWriter) {
	if r := recover(); r != nil {
		log.Println("Recovered from panic:", r)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// --- HTTP Handlers ---
func entriesHandler(w http.ResponseWriter, r *http.Request) {
	defer recoverPanic(w)
	// For PUT and DELETE, the ID is in the URL path
	id := strings.TrimPrefix(r.URL.Path, "/api/entries/")

	switch r.Method {
	case http.MethodGet:
		entries := entryStore.GetAll()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)

	case http.MethodPost:
		var entry SavedEntry
		if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		newEntry, err := entryStore.Add(entry)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(newEntry)

	case http.MethodPut:
		if id == "" {
			http.Error(w, "Missing entry ID", http.StatusBadRequest)
			return
		}
		var entry SavedEntry
		if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := entryStore.Update(id, entry); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Entry updated")

	case http.MethodDelete:
		if id == "" {
			http.Error(w, "Missing entry ID", http.StatusBadRequest)
			return
		}
		if err := entryStore.Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Entry deleted")

	default:
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
	}
}

func handleReannounce(w http.ResponseWriter, r *http.Request) {
	defer recoverPanic(w)

	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	var cmd *exec.Cmd
	if devMode {
		log.Println("Dev mode: running script from filesystem.")
		cwd, err := os.Getwd()
		if err != nil {
			http.Error(w, "Failed to get current working directory: "+err.Error(), http.StatusInternalServerError)
			return
		}
		scriptPath := filepath.Join(cwd, "py", "force_reannounce.py")
		cmd = exec.Command("python3", scriptPath)
	} else {
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

// --- Main Function ---
func main() {
	var port string
	var defaultPeerAddr string
	var dbFile string

	flag.BoolVar(&devMode, "dev", false, "Enable development mode to load files from disk")
	flag.StringVar(&port, "port", "5000", "Port to listen on")
	flag.StringVar(&defaultPeerAddr, "addr", "127.0.0.1:63219", "Default peer address for the web UI input")
	flag.StringVar(&dbFile, "db", "entries.json", "Database file for saved entries")
	flag.Parse()

	var err error
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

	// Initialize the entry store
	entryStore, err = NewEntryStore(dbFile)
	if err != nil {
		log.Fatalf("Failed to initialize entry store: %v", err)
	}

	// --- Register HTTP Handlers ---
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method == http.MethodGet {
			data := struct {
				DefaultPeerAddr string
				Entries         []SavedEntry
			}{
				DefaultPeerAddr: defaultPeerAddr,
				Entries:         entryStore.GetAll(),
			}
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

	// API handler for entries
	http.HandleFunc("/api/entries/", entriesHandler)

	log.Printf("Starting server on port: %s...", port)
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, nil))
}
