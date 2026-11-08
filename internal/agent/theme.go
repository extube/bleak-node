// Theme catalog for agent chats. Every chat is assigned one theme; its
// scenario drives three prompt fragments: the hello prompt (opening turn),
// the interaction context (every middle turn) and the goodbye prompt (the
// abstract farewell turn, generated without the transcript). The catalog
// holds ABSTRACT discussion themes - technology, economy, culture,
// philosophy and everyday city life - that fit any two common people:
// every agent brings its own ordinary work, study and hobbies into the
// debate, the theme itself never implies a profession.
package agent

// Scenario holds the three prompt fragments of a chat scenario:
// "prompt for hello message", "prompt for interaction context" and
// "prompt for goodbye".
type Scenario struct {
	// Hello directs the very first message of the chat (performed once;
	// the chat carries no transcript at that point).
	Hello string
	// Context directs the middle of the conversation; it is injected into
	// every turn prompt next to the transcript.
	Context string
	// Goodbye directs the farewell turn. The farewell is generated WITHOUT
	// any earlier context: it must be abstract and polite, like the natural
	// ending of an everyday conversation.
	Goodbye string
}

// Theme is one topic of the catalog with its scenario.
type Theme struct {
	// Key is the stable id persisted on the chat row ("ai-future"...).
	Key string
	// Name is the short human-readable title of the theme.
	Name string
	// Description explains the theme to both agents.
	Description string
	// Scenario holds the three prompt fragments.
	Scenario Scenario
}

// debateToast is appended to every context directive: it keeps the agents
// debating like two curious everyday people who discuss the theme through
// their own ordinary work and hobbies instead of talking like experts.
const debateToast = "You are two curious everyday people, not experts: look at the theme from your own life - your work, study, hobbies, small daily experience - while your partner gives their opposite angle with their own. Take turns: give an example or opinion, then ask a short light question about the partner's view. Do NOT circle one narrow point; move across different sides of the theme and keep it friendly, not professor-like."

// Themes is the fixed catalog of chat themes (topic + scenario). The worker
// picks one for every new chat.
var Themes = []Theme{
	{
		Key:         "ai-future",
		Name:        "Artificial intelligence and progress",
		Description: "Where intelligent machines are taking everyday life: human work, free time and what progress really means.",
		Scenario: Scenario{
			Hello:   "After the introduction, say you have been thinking about artificial intelligence lately and ask the partner's first blunt opinion about it.",
			Context: "Keep the debate on AI and its future: what machines may take over, what stays human, the strange hopes and fears around it. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "economic-crises",
		Name:        "Economic crises: why and how to prevent",
		Description: "Why economies pulse with crises - and what ordinary people, companies and states might change to soften them.",
		Scenario: Scenario{
			Hello:   "After the introduction, bring up how prices and money changes recently feel, and ask the partner if they sense another crisis coming.",
			Context: "Keep the debate on economic crises: why they begin, who feels them first, what could prevent the next one. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "philosophy-happiness",
		Name:        "What happiness actually is",
		Description: "The old question in everyday terms: is happiness a thing to chase, a habit, or a calm by-product?",
		Scenario: Scenario{
			Hello:   "After the introduction, tell that some small moment recently made you unreasonably happy, and ask what the partner thinks happiness even is.",
			Context: "Keep the debate on what happiness is: quiet mornings versus big goals, comparing with other people, keeping moments. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "creativity-source",
		Name:        "Where good ideas come from",
		Description: "That mystery of human creativity: sudden inspiration, hard routine, or copying with love?",
		Scenario: Scenario{
			Hello:   "After the introduction, mention a small clever solution you saw recently, and ask the partner where they think ideas really come from.",
			Context: "Keep the debate on creativity: inspiration versus discipline, ideas at odd hours, imitation and novelty. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "big-city-life",
		Name:        "Big city: freedom or noise",
		Description: "The bright sides and the heavy sides of living in a metropolis - anonymity, chances and the price of both.",
		Scenario: Scenario{
			Hello:   "After the introduction, tell what you honestly love and what secretly tires you about your city, and ask the partner the same.",
			Context: "Keep the debate on big city life: crowds and chances, noise and quiet corners, moving to a smaller place - to or to stay. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "cinema-culture",
		Name:        "Cinema in the age of the feed",
		Description: "Films, series and short videos: what happens to attention and culture when everything becomes a scroll.",
		Scenario: Scenario{
			Hello:   "After the introduction, admit what you last watched with real attention, and ask how the partner actually watches films nowadays.",
			Context: "Keep the debate on cinema and culture today: long films vs short clips, theatres vs screens at home, what art needs to survive. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "music-generations",
		Name:        "Why every generation has its music",
		Description: "The songs we hold from youth, why tastes harden with age, and how music keeps generations apart and together.",
		Scenario: Scenario{
			Hello:   "After the introduction, confess the song you unexpectedly heard this morning, and ask what music the partner would defend to the end.",
			Context: "Keep the debate on music and generations: playlists of your years, concerts against headphones, does good taste exist. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "work-life-balance",
		Name:        "Should work define a person",
		Description: "Making a living versus making a life: how much of yourself belongs in your job, and where the border sits.",
		Scenario: Scenario{
			Hello:   "After the introduction, mention one work thought that followed you home yesterday, and ask how the partner splits work from life.",
			Context: "Keep the debate on work and life balance: busy season stories, rest that restores, choosing a way of living. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "education-future",
		Name:        "What schools should really teach",
		Description: "Memory or skills: what children and adults of the 2030s should learn, and which subjects lost their value.",
		Scenario: Scenario{
			Hello:   "After the introduction, tell one useful and one useless thing that your own studies left you, and ask what the partner would teach children now.",
			Context: "Keep the debate on education: school against practice, exams, the internet inside the classroom, learning as adults. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "living-traditions",
		Name:        "Traditions worth keeping",
		Description: "Old habits of family and street: what deserves to survive the modern pace and what is fine to let go.",
		Scenario: Scenario{
			Hello:   "After the introduction, mention a tradition you keep even though it makes no sense to anyone, and ask the partner what they still keep.",
			Context: "Keep the debate on traditions: family rituals, holidays and their real meaning, inventing new traditions. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "space-exploration",
		Name:        "Why reach for the stars",
		Description: "Space programs in everyday economics: dreams for humanity, or money while problems rot on Earth?",
		Scenario: Scenario{
			Hello:   "After the introduction, mention the last space headline you half-believed, and ask whether the partner sees space as a dream or a bill.",
			Context: "Keep the debate on space: satellites in daily life, moon bases, the costs, what Earth gains from it. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "freedom-vs-security",
		Name:        "Freedom or comfort",
		Description: "The everyday trade: rules, cameras and forms that buy safety while shaving a bit of freedom and personality off.",
		Scenario: Scenario{
			Hello:   "After the introduction, tell one rule that annoys you for safety's sake, and ask where the partner would draw the line.",
			Context: "Keep the debate on freedom versus security: cameras, passwords, the soft rules of cities, life in the middle. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "language-evolution",
		Name:        "The language of messengers",
		Description: "How texting reshapes language: abbreviations, old meanings, silence as punctuation, and grammar rules on overtime.",
		Scenario: Scenario{
			Hello:   "After the introduction, ask the partner whether chat messages have become shorter than ever - and what that did to language.",
			Context: "Keep the debate on language change: texting style, borrowed words, voice notes against typing, the words that die of old age. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "modern-consumption",
		Name:        "Do we really need all this",
		Description: "Consumer habits of the day: buying to keep, buying to replace, and the quiet prestige of owning less.",
		Scenario: Scenario{
			Hello:   "After the introduction, confess the last thing you bought without any need, and ask how the partner resists or ignores the urge.",
			Context: "Keep the debate on consumption: sales and their charms, things loved and abandoned, second hands, the art of keeping less. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "urban-mobility",
		Name:        "How cities should move",
		Description: "Metro, bicycles, cars and feet: how people will move through the cities in ten years, and who must change first.",
		Scenario: Scenario{
			Hello:   "After the introduction, tell what your daily route to places looks like, and ask how the partner would redesign morning traffic.",
			Context: "Keep the debate on getting around the city: public transport, bikes, cars, walking cities, fairness of the streets. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "online-friendship",
		Name:        "Can online friends be real friends",
		Description: "Friendship when it lives in messengers: depth without a shared street, distance felt in typing dots.",
		Scenario: Scenario{
			Hello:   "After the introduction, mention a person you know only through messages, and ask the partner if they think such friendship counts.",
			Context: "Keep the debate on online friendship: calls against meetups, the kindness of typing strangers, keeping people close across distance. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "books-vs-screens",
		Name:        "Books against screens",
		Description: "The reading habit today: paper, e-ink, phone pages - and what happens to patience and memory along the way.",
		Scenario: Scenario{
			Hello:   "After the introduction, tell the last book you actually finished, and ask how the partner reads these days.",
			Context: "Keep the debate on reading: paper charm, screens that eat hours, finishing or abandoning, reading as a habit or an incident. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "climate-everyday",
		Name:        "The climate question of one person",
		Description: "What an ordinary person can honestly change: daily habits, prices, guilt tripping, and real impact.",
		Scenario: Scenario{
			Hello:   "After the introduction, mention one small eco habit you tried and abandoned, and ask what the partner thinks one person can even change.",
			Context: "Keep the debate on climate from a person's view: habits and their price, the town around it, big players against small steps. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "prepare-for-old-age",
		Name:        "How to grow old bravely",
		Description: "Health, money and respect: how ordinary people should prepare for age and what the state owes them.",
		Scenario: Scenario{
			Hello:   "After the introduction, mention one thing you are saving or learning 'for later age', and ask what the partner bets on already.",
			Context: "Keep the debate on aging: savings and habits, loneliness, work in the fifties, respect and boredom. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "craft-vs-mass",
		Name:        "Craft against the conveyor",
		Description: "Hand-made personality versus mass production: price, quality and why craft refuses to die.",
		Scenario: Scenario{
			Hello:   "After the introduction, mention something that was clearly made by hands, and ask whether the partner pays extra for craft.",
			Context: "Keep the debate on craft and mass: factories versus studios, repair culture, buying as voting. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "digital-privacy",
		Name:        "Is privacy already gone",
		Description: "Data you hand away for convenience: the leaking pinhole in the daily life of an ordinary user.",
		Scenario: Scenario{
			Hello:   "After the introduction, admit the amount of data a phone probably holds about you, and ask how the partner feels about it.",
			Context: "Keep the debate on privacy: passwords and cameras, tracking in apps, what privacy is worth today. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "generation-gap",
		Name:        "Why every generation complains",
		Description: "The endless loop: each generation calling the next one worse - from the typewriter to the feed.",
		Scenario: Scenario{
			Hello:   "After the introduction, mention a complaint your parents' generation said about yours, and ask whether the partner hears the same tones.",
			Context: "Keep the debate on generations: the young and the old, misunderstandings, inheritance of habits and jokes. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "what-home-means",
		Name:        "What makes a home a home",
		Description: "From the walls and mugs to the feeling: what actually turns a shelter into a home, and when home moves with you.",
		Scenario: Scenario{
			Hello:   "After the introduction, mention one small thing at your place that instantly calms you, and ask what makes the partner's flat a home.",
			Context: "Keep the debate on home: flats and their warmth, moving, decoration and empty corners, homesickness. " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
	{
		Key:         "luck-vs-effort",
		Name:        "Luck against effort",
		Description: "What drives an ordinary life: the hours you put in or the doors that happen to be open that day?",
		Scenario: Scenario{
			Hello:   "After the introduction, mention one lucky accident that actually turned into something, and ask how the partner splits luck from effort.",
			Context: "Keep the debate on luck and effort: hard work stories, chance meetings, fairness of both. Do NOT stay on one narrow point; " + debateToast,
			Goodbye: "The time to go comes. Say a short, abstract and polite goodbye like two people finishing an everyday chat.",
		},
	},
}
