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

	// UserJoinedGroupMessageID sent to all the other users in a group when someone joins
	UserJoinedGroupMessageID = 1001

	// UserLeftGroupMessageID sent to all the other users in a group when someone leaves
	UserLeftGroupMessageID = 1002

	// ConnectedMessageID sent to a user when they join the server, gives them a name
	ConnectedMessageID = 1100

	// RTCSessionDescriptionMessageID sent from a user when they want to trade sessiond descriptions
	RTCSessionDescriptionMessageID = 1200

	// RTCICECandidateMessageID sent from a user when they want to trade sessiond descriptions
	RTCICECandidateMessageID = 1201

	// BroadcastMessageID - sent from a user and broadcast to the others in their group
	BroadcastMessageID = 6666
)

const (
	// MaxPlayersInGroup - bloop
	MaxPlayersInGroup = 6
	PingTime          = 30
)

type user struct {
	sync.RWMutex
	Connection *websocket.Conn
	Username   string
	ID         uuid.UUID
	GroupID    string
	PingTicker *time.Ticker
}

type group struct {
	ID      string
	Members []uuid.UUID
}

type messageID struct {
	ID int `json:"id"`
}

type startJoinGroupMessage struct {
	ID    int    `json:"id"`
	Group string `json:"group"`
}

type userInfo struct {
	Username string `json:"name"`
	ID       string `json:"id"`
}

type joinedGroupMessage struct {
	ID      int        `json:"id"`
	Group   string     `json:"group"`
	Members []userInfo `json:"members"`
}

type userJoinedLeftGroupMessage struct {
	ID    int      `json:"id"`
	Group string   `json:"group"`
	User  userInfo `json:"userInfo"`
}

type connectedMessage struct {
	ID   int      `json:"id"`
	User userInfo `json:"userInfo"`
}

type rtcForwardedMessage struct {
	ID           int    `json:"id"`
	SourceUserID string `json:"source"`
	TargetUserID string `json:"target"`
}

var upgrade = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     checkOrigin,
}

var address = flag.String("address", ":3123", "HTTP Server Address")
var local = flag.Bool("local", false, "Are we running locally or not. Disables origin checks etc.")

var nextUserID uint64 = 0

var users = struct {
	sync.RWMutex
	UserMap map[uuid.UUID]*user
}{UserMap: make(map[uuid.UUID]*user)}

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
			Members: make([]uuid.UUID, 0, MaxPlayersInGroup),
		}

		groups.GroupMap[groupID] = g

		log.Printf("Created Group: %v", groupID)
	}

	if len(g.Members) == MaxPlayersInGroup {
		// todo send full message
		return
	}

	joinedMessage := joinedGroupMessage{
		ID:      JoinedGroupMessageID,
		Group:   groupID,
		Members: make([]userInfo, 0, MaxPlayersInGroup),
	}

	for _, userID := range g.Members {
		users.RLock()
		user := users.UserMap[userID]
		users.RUnlock()

		user.RLock()
		userInfo := userInfo{
			ID:       user.ID.String(),
			Username: user.Username,
		}
		user.RUnlock()

		joinedMessage.Members = append(joinedMessage.Members, userInfo)
	}

	u.Lock()
	u.Connection.WriteJSON(joinedMessage)
	u.GroupID = groupID
	u.Unlock()

	u.RLock()
	groupMessage := userJoinedLeftGroupMessage{
		ID:    UserJoinedGroupMessageID,
		Group: groupID,
		User: userInfo{
			ID:       u.ID.String(),
			Username: u.Username,
		},
	}
	u.RUnlock()

	for _, memberUUID := range g.Members {
		users.RLock()
		member := users.UserMap[memberUUID]
		users.RUnlock()

		if member == nil {
			continue
		}

		member.Lock()
		member.Connection.WriteJSON(groupMessage)
		member.Unlock()
	}

	g.Members = append(g.Members, u.ID)

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
		return
	}

	u.RLock()
	groupMessage := userJoinedLeftGroupMessage{
		ID:    UserLeftGroupMessageID,
		Group: group.ID,
		User: userInfo{
			ID:       u.ID.String(),
			Username: u.Username,
		},
	}
	u.RUnlock()

	for _, memberUUID := range group.Members {
		users.RLock()
		member := users.UserMap[memberUUID]
		users.RUnlock()

		if member == nil {
			continue
		}

		member.Lock()
		member.Connection.WriteJSON(groupMessage)
		member.Unlock()
	}
}

func checkOrigin(request *http.Request) bool {
	if *local {
		return true
	}

	origin := request.Header.Get("Origin")
	log.Printf("Origin from: %v", origin)
	return origin == "https://returnofthefist.com"
}

func handleRequest(writer http.ResponseWriter, request *http.Request) {
	log.Print("Incoming connection")
	connection, err := upgrade.Upgrade(writer, request, nil)
	if err != nil {
		log.Print("Failed to upgrade connection to WebSockets: ", err)
		return
	}

	client := user{
		Connection: connection,
		Username:   fmt.Sprintf("Player_%d", atomic.AddUint64(&nextUserID, 1)),
		ID:         uuid.New(),
		PingTicker: time.NewTicker(PingTime * time.Second),
	}

	users.Lock()
	users.UserMap[client.ID] = &client
	users.Unlock()

	connectMessage := connectedMessage{
		ID: ConnectedMessageID,
		User: userInfo{
			ID:       client.ID.String(),
			Username: client.Username,
		},
	}

	client.Lock()
	client.Connection.WriteJSON(connectMessage)
	client.Unlock()

	go client.processPing()
	go client.processMessages()
}

func (u *user) processPing() {
	for {
		<-u.PingTicker.C
		u.Lock()
		u.Connection.WriteMessage(websocket.PingMessage, nil)
		u.Unlock()
	}
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

			if message.Group == "" {
				log.Printf("No group provided to start or join.")
				continue
			}

			addUserToGroup(message.Group, u)
		case RTCSessionDescriptionMessageID, RTCICECandidateMessageID:
			var message rtcForwardedMessage
			json.Unmarshal(bytes, &message)

			targetUUID, _ := uuid.Parse(message.TargetUserID)
			users.RLock()
			target := users.UserMap[targetUUID]
			users.RUnlock()

			if target == nil {
				return
			}

			target.Lock()
			target.Connection.WriteMessage(websocket.TextMessage, bytes)
			target.Unlock()
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
