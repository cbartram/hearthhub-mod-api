package server

import (
	"encoding/json"
	"fmt"
	"github.com/cbartram/hearthhub-mod-api/server/service"
	amqp "github.com/rabbitmq/amqp091-go"
	log "github.com/sirupsen/logrus"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/websocket"
)

// Message represents the structure of messages being passed
type Message struct {
	Type      string      `json:"type"`
	Content   interface{} `json:"content"`
	DiscordId string      `json:"discord_id"`
}

// Client represents a WebSocket client connection
type Client struct {
	conn      *websocket.Conn
	queueName string
	discordId string
}

// WebSocketManager handles multiple WebSocket connections
type WebSocketManager struct {
	Channel    *amqp.Channel
	clients    map[*Client]bool
	broadcast  chan Message
	register   chan *Client
	unregister chan *Client
	mutex      sync.Mutex
}

// NewWebSocketManager creates a new WebSocket manager
func NewWebSocketManager() (*WebSocketManager, error) {
	// Connect to RabbitMQ
	credentials := fmt.Sprintf("%s:%s", os.Getenv("RABBITMQ_DEFAULT_USER"), os.Getenv("RABBITMQ_DEFAULT_PASS"))
	conn, err := amqp.Dial(fmt.Sprintf("amqp://%s@%s/", credentials, os.Getenv("RABBITMQ_BASE_URL")))
	if err != nil {
		log.Errorf("failed to connect to RabbitMQ: %v", err)
		return nil, err
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		log.Errorf("failed to open channel: %v", err)
	}
	defer ch.Close()

	err = ch.ExchangeDeclare(
		"valheim-server-status", // exchange name
		"fanout",                // exchange type
		true,                    // durable
		false,                   // auto-deleted
		false,                   // internal
		false,                   // no-wait
		nil,                     // arguments
	)
	if err != nil {
		log.Fatalf("failed to declare exchange: %v", err)
	}

	return &WebSocketManager{
		Channel:    ch,
		clients:    make(map[*Client]bool),
		broadcast:  make(chan Message),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}, nil
}

// Run Listens to go routine channels for websocket events when clients
// connect, disconnect, or broadcast a message. This function keeps track
// of client state like who is connected and disconnected
func (w *WebSocketManager) Run() {
	for {
		select {
		case client := <-w.register:
			w.mutex.Lock()
			w.clients[client] = true
			w.mutex.Unlock()
			log.Infof("client connected with discord ID: %s", client.discordId)

		case client := <-w.unregister:
			if _, ok := w.clients[client]; ok {
				w.mutex.Lock()
				delete(w.clients, client)
				client.conn.Close()
				w.mutex.Unlock()
				log.Infof("client disconnected with discord ID: %s", client.discordId)
			}
		}
	}
}

func (w *WebSocketManager) HandleWebSocket(user *service.CognitoUser, writer http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // Allow all origins in development
		},
	}

	conn, err := upgrader.Upgrade(writer, r, nil)
	if err != nil {
		log.Printf("Error upgrading connection: %v", err)
		return
	}

	// Declare a unique queue for this connection
	q, err := w.Channel.QueueDeclare(
		"",    // empty name for auto-generated name
		false, // non-durable
		true,  // delete when unused
		true,  // exclusive
		false, // no-wait
		nil,   // arguments
	)
	if err != nil {
		log.Printf("Error declaring queue: %v", err)
		conn.Close()
		return
	}

	// Bind the queue to the exchange with server-specific routing key
	err = w.Channel.QueueBind(
		q.Name,                  // queue name
		user.DiscordID,          // routing key (specific discord ID)
		"valheim-server-status", // exchange
		false,
		nil,
	)
	if err != nil {
		log.Printf("Error binding queue: %v", err)
		conn.Close()
		return
	}

	// Start consuming from the queue
	msgs, err := w.Channel.Consume(
		q.Name, // queue
		"",     // consumer
		true,   // auto-ack
		true,   // exclusive
		false,  // no-local
		false,  // no-wait
		nil,    // args
	)
	if err != nil {
		log.Printf("Error starting consumer: %v", err)
		conn.Close()
		return
	}

	client := &Client{
		conn:      conn,
		queueName: q.Name,
		discordId: user.DiscordID,
	}

	w.register <- client

	// Every client get's their own QueueBind which is routed by the discord id.
	// This is why no broadcasting or checking if message discord id = client id is needed
	// Client's will only consume their messages since they are only sent to their discord id.
	go func() {
		for msg := range msgs {
			var message Message
			log.Infof("Message received: %s", msg.Type)
			if err := json.Unmarshal(msg.Body, &message); err != nil {
				log.Errorf("error unmarshaling message: %v", err)
				continue
			}

			err := client.conn.WriteJSON(message)
			if err != nil {
				log.Errorf("error sending message to websocket: %v", err)
				return
			}
		}
	}()

	defer func() {
		w.unregister <- client
		conn.Close()
	}()

	// Read messages
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}
