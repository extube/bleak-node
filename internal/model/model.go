package model

import "time"

// Room is a chatroom / location addressed by a unique address. The room
// language (en or ru) is the single language of everything inside it: the
// users' greetings, their chats and every message.
type Room struct {
	Address     string     `json:"address" example:"0xroom123"`
	Name        string     `json:"name" example:"Meta Room"`
	Description string     `json:"description" example:"A room for bleak meetings"`
	Language    string     `json:"language" example:"en"`
	MaxUser     int        `json:"max_user" example:"100"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at"`
}

// User is a person linked to a room. The user language is inherited from
// the room and defines the language of the greeting (hello) message.
// Active marks the persona as available for the auto-matchmaking worker
// (feature #36): only active users enter the matching matrix and receive
// automatic handshakes.
type User struct {
	Address    string `json:"address" example:"0xuser123"`
	Name       string `json:"name" example:"Alice"`
	Room       string `json:"room" example:"0xroom123"`
	PublicBio  string `json:"public_bio" example:"public hello"`
	PrivateBio string `json:"private_bio" example:"real me"`
	Greeting   string `json:"greeting" example:"Hi, I'm Alice — I paint watercolor postcards."`
	Language   string `json:"language" example:"en"`
	// Active users are matched by the auto-matchmaking worker; the flag is
	// true by default and togglable via the user update endpoint.
	Active    bool       `json:"active" example:"true"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at"`
	// MutualCodeLive reports whether a one-time mutual code is registered
	// for the persona (derived from the stored hash, never stored itself).
	// It is set ONLY on token-authenticated reads of the persona; public
	// reads leave it nil.
	MutualCodeLive *bool `json:"mutual_code_live,omitempty" example:"true"`
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

// Interest is one declared interest of a user: a short title classified by
// a numeric group (the interest category) and a numeric key (the concrete
// interest inside that category). Keys are unique per user: re-adding an
// existing key replaces the title/group. Interests feed the matching score
// (POST /api/match).
type Interest struct {
	UserAddress string `json:"user_address" example:"0xuser123"`
	Title       string `json:"title" example:"Chess"`
	Group       int    `json:"group" example:"1"`
	Key         int    `json:"key" example:"7"`
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
	Language       string     `json:"language" example:"en"`
	Phase          string     `json:"phase" example:"networking"`
	PhaseStartedAt *time.Time `json:"phase_started_at"`
	// Done reports that the conversation reached its final goodbye: the
	// farewell turn stamps it automatically; a kickoff resets it to false.
	Done      bool      `json:"done" example:"false"`
	CreatedAt time.Time `json:"created_at"`
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
	// NotBefore is the earliest moment the event may start again (retry
	// backoff); nil means immediately eligible.
	NotBefore *time.Time `json:"not_before,omitempty"`
}

// Contact is a contact detail (messenger handle, email, ...) shared once
// from one persona to another. The (from, to) pair is the unique key: a
// persona can share its contacts with a given user only once, and only the
// recipient (to) may read the record.
type Contact struct {
	From      string    `json:"from" example:"0xuser123"`
	To        string    `json:"to" example:"0xuser456"`
	Content   string    `json:"content" example:"telegram: @alice_paints"`
	CreatedAt time.Time `json:"created_at"`
}

// Summary is the hard-matching result (feature #35): the summarized chat of
// a pair, the merged match score, and the networking checklist of closed
// checkpoints. One record per user pair; a repeated hard-match request
// returns the stored record.
type Summary struct {
	Chat  string `json:"chat" example:"0xchat123"`
	Room  string `json:"room" example:"0xroom123"`
	UserA string `json:"user_a" example:"0xuser123"`
	UserB string `json:"user_b" example:"0xuser456"`
	// Score is the merged hard-match score, strictly inside (0, 100).
	Score       float64      `json:"score" example:"66.4"`
	Checkpoints []Checkpoint `json:"checkpoints"`
	// Description is the chat summarization produced by the hard match.
	Description string    `json:"description" example:"Alice and Bob introduced themselves, found a shared love of watercolor and closed with a warm goodbye."`
	CreatedAt   time.Time `json:"created_at"`
}

// Checkpoint is one networking criterion of the hard-match checklist with
// its closed flag.
type Checkpoint struct {
	Key    string `json:"key" example:"introductions"`
	Label  string `json:"label" example:"Both introduced themselves"`
	Closed bool   `json:"closed" example:"true"`
}

// Networking checkpoint keys (feature #35). The canonical list and its
// labels live here; the summaries table stores only the closed flags and
// the rehydration fills the labels back from this list.
const (
	CheckpointSharedIdea       = "shared_idea"
	CheckpointExpertise        = "expertise_exchange"
	CheckpointReciprocalDialog = "reciprocal_dialog"
)

// NetworkingCheckpoints is the fixed checklist the hard match grades: every
// entry marks one criterion of a good networking conversation. The three
// criteria are deliberately demanding (feature #43): they grade the
// NETWORKING value of the dialog, not its politeness.
func NetworkingCheckpoints(language string) []Checkpoint {
	ru := language == "ru"
	return []Checkpoint{
		{Key: CheckpointSharedIdea,
			Label: ternaryStr(ru,
				"По итогам беседы удалось сформулировать перспективную идею, требующую дальнейшей проработки",
				"Left the conversation with a new idea or insight worth developing further"),
			Closed: false},
		{Key: CheckpointExpertise,
			Label: ternaryStr(ru,
				"Каждая сторона узнала что-то конкретное о работе, навыках или проектах другой",
				"Each side learned something concrete about the other's work, skills or projects"),
			Closed: false},
		{Key: CheckpointReciprocalDialog,
			Label: ternaryStr(ru,
				"Обе стороны задавали уточняющие вопросы и внесли примерно равный вклад в разговор",
				"Both sides asked follow-up questions and contributed roughly equally"),
			Closed: false},
	}
}

// ternaryStr returns a when ok, else b.
func ternaryStr[T any](ok bool, a, b T) T {
	if ok {
		return a
	}
	return b
}

// Validation limits.
const (
	// MaxMessageContentLen is the maximum number of symbols in message content.
	MaxMessageContentLen = 255
	// MaxInterestTitleLen is the maximum number of symbols in an interest title.
	MaxInterestTitleLen = 64
	// MaxInterestsPerRequest bounds the bulk add/delete payload size.
	MaxInterestsPerRequest = 50
	// MaxContactContentLen is the maximum number of symbols in shared
	// contact content.
	MaxContactContentLen = 255
	// MaxSummaryDescriptionLen is the maximum number of symbols in a
	// stored chat summarization.
	MaxSummaryDescriptionLen = 1000
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
