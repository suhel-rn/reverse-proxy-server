package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

type Client struct {
	ID         string
	Connection *websocket.Conn
	LastPing   time.Time
}

type ClientResponse struct {
	Data      string
	Timestamp time.Time
}

var (
	clients      = make(map[string]*Client)
	clientsMutex sync.RWMutex
	cache        = make(map[string]ClientResponse)
	cacheMutex   sync.RWMutex
	upgrader     = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
)

func main() {
	r := mux.NewRouter()
	r.HandleFunc("/register", handleRegister).Methods("POST")
	r.HandleFunc("/connect", handleWebSocket)
	r.HandleFunc("/query/{clientID}", handleQuery).Methods("GET")

	go cleanupInactiveClients()

	port := ":8080"

	log.Printf("Server starting on port %s", port)
	log.Fatal(http.ListenAndServe(port, r))
}

func handleRegister(w http.ResponseWriter, r *http.Request) {
	var registration struct {
		ClientID string `json:"client_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	scheme := "ws"
	if r.TLS != nil {
		scheme = "wss"
	}

	connectionUrl := fmt.Sprintf("%s://%s/connect?client_id=%s", scheme, r.Host, registration.ClientID)

	response := struct {
		ConnectionUrl string `json:"connection_url"`
	}{
		ConnectionUrl: connectionUrl,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	clientID := r.URL.Query().Get("client_id")
	if clientID == "" {
		http.Error(w, "client_id query parameter is required", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	client := &Client{
		ID:         clientID,
		Connection: conn,
		LastPing:   time.Now(),
	}

	clientsMutex.Lock()
	clients[clientID] = client
	clientsMutex.Unlock()

	log.Printf("Client connected: %s", clientID)

	go handleClientMessages(client)
}

func handleQuery(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	clientID := vars["clientID"]

	cacheMutex.RLock()
	cachedResponse, exists := cache[clientID]
	cacheMutex.RUnlock()

	if exists && time.Since(cachedResponse.Timestamp) < 5*time.Second {
		w.Write([]byte(cachedResponse.Data))
		return
	}

	clientsMutex.RLock()
	client, exists := clients[clientID]
	clientsMutex.RUnlock()

	if !exists {
		http.Error(w, "Client not connected", http.StatusNotFound)
		return
	}

	err := client.Connection.WriteMessage(websocket.TextMessage, []byte("GET_DATA"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_, message, err := client.Connection.ReadMessage()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	cacheMutex.Lock()
	cache[clientID] = ClientResponse{Data: string(message), Timestamp: time.Now()}
	cacheMutex.Unlock()

	w.Write(message)
}

func handleClientMessages(client *Client) {
	defer func() {
		client.Connection.Close()
		clientsMutex.Lock()
		delete(clients, client.ID)
		clientsMutex.Unlock()
		log.Printf("Client disconnected: %s", client.ID)
	}()

	for {
		_, message, err := client.Connection.ReadMessage()
		if err != nil {
			log.Printf("Error reading message from client %s: %v", client.ID, err)
			break
		}

		client.LastPing = time.Now()

		cacheMutex.Lock()
		cache[client.ID] = ClientResponse{Data: string(message), Timestamp: time.Now()}
		cacheMutex.Unlock()
	}
}

func cleanupInactiveClients() {
	for {
		time.Sleep(1 * time.Minute)

		now := time.Now()
		clientsMutex.Lock()
		for id, client := range clients {
			if now.Sub(client.LastPing) > 2*time.Minute {
				client.Connection.Close()
				delete(clients, id)
				log.Printf("Client %s was inactive and was disconnected", id)
			}
		}
		clientsMutex.Unlock()
	}
}
