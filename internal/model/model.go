package model

import "time"

// Room is a chatroom / location addressed by a unique address.
type Room struct {
	Address     string     `json:"address" example:"0xroom123"`
	Name        string     `json:"name" example:"Meta Room"`
	Description string     `json:"description" example:"A room for bleak meetings"`
	MaxUser     int        `json:"max_user" example:"100"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at"`
}

// User is a person linked to a room.
type User struct {
	Address    string     `json:"address" example:"0xuser123"`
	Name       string     `json:"name" example:"Alice"`
	Room       string     `json:"room" example:"0xroom123"`
	PublicBio  string     `json:"public_bio" example:"public hello"`
	PrivateBio string     `json:"private_bio" example:"real me"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at"`
}

// Handshake is a directed, valued interaction between two parties within the
// same room (the system creates the first handshake room -> user).
type Handshake struct {
	TxID        string    `json:"tx_id" example:"0xtx42"`
	FromAddress string    `json:"from_address" example:"0xuser123"`
	ToAddress   string    `json:"to_address" example:"0xuser456"`
	Room        string    `json:"room" example:"0xroom123"`
	Status      int       `json:"status" example:"1"`
	Value       int       `json:"value" example:"1"`
	CreatedAt   time.Time `json:"created_at"`
}

// Chat links two users who share messages. Created automatically once
// handshakes exist in both directions (A -> B and B -> A). Chats live in
// the same room as their participants. Every chat carries a scenario key
// (the debate theme of part 2) and a conversation phase; phase_started_at
// marks the moment part 2 began, so the networking part stays visible in
// the API while the debate prompts only replay part 2 messages.
type Chat struct {
	Address        string     `json:"address" example:"0xchat123"`
	Room           string     `json:"room" example:"0xroom123"`
	Scenario       string     `json:"scenario" example:"ai-future"`
	Phase          string     `json:"phase" example:"networking"`
	PhaseStartedAt *time.Time `json:"phase_started_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

// Participant is one of the two chat members; unique key is (chat, user).
type Participant struct {
	Chat string `json:"chat" example:"0xchat123"`
	User string `json:"user" example:"0xuser123"`
}

// Message is a text exchange inside a chat.
type Message struct {
	TxID      string    `json:"tx_id" example:"0x9f1c0d..."`
	From      string    `json:"from" example:"0xuser123"`
	To        string    `json:"to" example:"0xuser456"`
	CreatedAt time.Time `json:"created_at"`
	Content   string    `json:"content" example:"hello there"`
	Chat      string    `json:"chat" example:"0xchat123"`
}

// Event is one entry of the lightweight queue (SQLite-backed) that drives
// the automatic agent interaction inside a chat.
type Event struct {
	ID          int64      `json:"id" example:"1"`
	Type        string     `json:"type" example:"chat.interact"`
	Chat        string     `json:"chat" example:"0xchat123"`
	Speaker     string     `json:"speaker" example:"0xuser123"`
	Listener    string     `json:"listener" example:"0xuser456"`
	Status      string     `json:"status" example:"pending"`
	Attempts    int        `json:"attempts" example:"0"`
	Error       string     `json:"error" example:""`
	CreatedAt   time.Time  `json:"created_at"`
	ProcessedAt *time.Time `json:"processed_at"`
}

// Validation limits.
const (
	// MaxMessageContentLen is the maximum number of symbols in message content.
	MaxMessageContentLen = 255
)

// Queue event types.
const (
	// EventChatInteract asks the worker to generate the next message of the
	// event speaker inside the chat.
	EventChatInteract = "chat.interact"
)

// Chat phase names: part 1 is the networking conversation (agents follow
// the networking checklist), part 2 the abstract theme debate that follows
// a context boundary.
const (
	// PhaseNetworking is the opening phase: agents introduce themselves
	// naturally, look for shared interests and build human trust.
	PhaseNetworking = "networking"
	// PhaseDebate is the second phase: a themed discussion after the
	// conversation history of part 1 has been sealed from the prompts.
	PhaseDebate = "debate"
)

// Agent interaction defaults.
const (
	// UserMessageLimit bounds the messages ONE participant writes in a
	// chat, the farewell included: no user sends more than
	// DefaultUserMessageLimit messages, so a dialog holds no more than
	// two such quotas in total.
	MinUserMessageLimit = 6
	MaxUserMessageLimit = 12
	// DefaultUserMessageLimit is the fallback when the operator sets nothing.
	DefaultUserMessageLimit = 12
)

// Balance constants for the handshake economy.
const (
	// DefaultRoomMaxUsers is assigned to every room as its max_user.
	DefaultRoomMaxUsers = 100
	// FirstHandshakeValue is the value of the auto handshake created for a new user.
	FirstHandshakeValue = 10
	// HandshakeValue is the fixed value of a user-to-user handshake.
	HandshakeValue = 1
)

// Balance / status defaults.
const (
	// DefaultStatus is the status assigned to a system-generated first handshake.
	DefaultStatus = 1
)
