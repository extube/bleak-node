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

// Handshake is a directed, valued interaction between two parties
// (usually user -> user; the system creates the first handshake room -> user).
type Handshake struct {
	TxID        string    `json:"tx_id" example:"0xtx42"`
	FromAddress string    `json:"from_address" example:"0xuser123"`
	ToAddress   string    `json:"to_address" example:"0xuser456"`
	Status      int       `json:"status" example:"1"`
	Value       int       `json:"value" example:"1"`
	CreatedAt   time.Time `json:"created_at"`
}

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
