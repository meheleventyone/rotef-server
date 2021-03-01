package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	// TestMessageID my weak ass test message
	TestMessageID = 666

	// StartJoinGroupMessageID sent from a user who wants to start or join a group
	StartJoinGroupMessageID = 100

	// JoinedGroupMessageID sent to a user when they join a group
	JoinedGroupMessageID = 1000

	// ConnectedMessageID sent to a user when they join the server, gives them a name
	ConnectedMessageID = 1001
)

const (
	// MaxPlayersInGroup - bloop
	MaxPlayersInGroup = 6
)

type user struct {
	sync.RWMutex
	Connection *websocket.Conn
	Username   string
	ID         string
	GroupID    string
}

type group struct {
	ID      string
	Members []string
}

type messageID struct {
	ID int `json:"id"`
}

type startJoinGroupMessage struct {
	ID    int    `json:"id"`
	Group string `json:"group"`
}

type connectedMessage struct {
	ID       int    `json:"id"`
	Username string `json:"name"`
	UserID   string `json:"userid"`
}

var upgrade = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     checkOrigin,
}

var address = flag.String("address", "localhost:8081", "HTTP Server Address")

var nextUserID uint64 = 0

var users = struct {
	sync.RWMutex
	UserMap map[string]*user
}{UserMap: make(map[string]*user)}

var groups = struct {
	sync.RWMutex
	GroupMap map[string]*group
}{
	GroupMap: make(map[string]*group),
}

func addUserToGroup(groupID string, u *user) {
	if groupID == "" {
		log.Printf("Warning tried to add %v, %v to an empty group id", u.ID, u.Username)
		return
	}

	removeUserFromGroup(u)

	groups.Lock()
	defer groups.Unlock()

	g := groups.GroupMap[groupID]

	if g == nil {
		g = &group{
			ID:      groupID,
			Members: make([]string, 0, MaxPlayersInGroup),
		}

		groups.GroupMap[groupID] = g

		log.Printf("Created Group: %v", groupID)
	}

	g.Members = append(g.Members, u.ID)

	u.Lock()
	u.GroupID = groupID
	u.Unlock()

	log.Printf("Added user %v %v to group %v", u.ID, u.Username, u.GroupID)
}

func removeUserFromGroup(u *user) {
	u.RLock()
	if u.GroupID == "" {
		u.RUnlock()
		return
	}
	log.Printf("Removing user %v %v from group %v", u.ID, u.Username, u.GroupID)
	u.RUnlock()

	groups.Lock()
	defer groups.Unlock()

	group := groups.GroupMap[u.GroupID]

	removeIdx := -1
	for idx, memberID := range group.Members {
		if memberID == u.ID {
			removeIdx = idx
			break
		}
	}

	if removeIdx >= 0 {
		group.Members[removeIdx] = group.Members[len(group.Members)-1]
		group.Members = group.Members[:len(group.Members)-1]
	}

	if len(group.Members) == 0 {
		log.Printf("Removing empty group: %v", group.ID)
		delete(groups.GroupMap, group.ID)
	}
}

func checkOrigin(request *http.Request) bool {
	// todo make this better
	return true
}

func handleRequest(writer http.ResponseWriter, request *http.Request) {
	connection, err := upgrade.Upgrade(writer, request, nil)
	if err != nil {
		log.Print("Failed to upgrade connection to WebSockets: ", err)
		return
	}

	client := user{
		Connection: connection,
		Username:   fmt.Sprintf("Player_%d", atomic.AddUint64(&nextUserID, 1)),
		ID:         uuid.NewString(),
	}

	users.Lock()
	users.UserMap[client.ID] = &client
	users.Unlock()

	connectMessage := connectedMessage{
		ID:       ConnectedMessageID,
		Username: client.Username,
		UserID:   client.ID,
	}
	client.Connection.WriteJSON(connectMessage)

	go client.processMessages()
}

func (u *user) processMessages() {
	// todo properly handle ping-pong, for now never time out
	u.Connection.SetReadDeadline(time.Time{})
	u.Connection.SetWriteDeadline(time.Time{})

	for {
		messageType, bytes, err := u.Connection.ReadMessage()

		if err != nil {
			log.Printf("Closing session due to error: %v", err)
			break
		}

		if messageType == websocket.BinaryMessage {
			continue
		}

		var checkMessageType messageID
		json.Unmarshal(bytes, &checkMessageType)

		switch checkMessageType.ID {
		case StartJoinGroupMessageID:
			var message startJoinGroupMessage
			json.Unmarshal(bytes, &message)

			addUserToGroup(message.Group, u)
		}
	}

	// when this goroutine ends the user is no more so cleanup
	removeUserFromGroup(u)

	users.Lock()
	delete(users.UserMap, u.ID)
	users.Unlock()
}

func main() {
	flag.Parse()
	log.Print("Welcome to the awesome Return of the Exploding Fist Server Infrastructure")

	http.HandleFunc("/", handleRequest)

	err := http.ListenAndServe(*address, nil)
	if err != nil {
		log.Fatal("Server errored out: ", err)
	}
}
