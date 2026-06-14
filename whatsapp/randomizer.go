package whatsapp

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/whatpilot/backend/models"
)

// triggerEmojiSets maps each trigger type to emoji sets appropriate for that event.
// Using context-aware emojis avoids absurd combinations like "Order Cancelled 🎉".
var triggerEmojiSets = map[models.TriggerType][][]string{
	models.TriggerOrderCreated: {
		{"🛍️", "🛒", "🎁", "🏷️", "📦"},
		{"✅", "👍", "🙌", "🤝", "💪"},
		{"🌟", "⭐", "💫", "✨", "🎊"},
		{"😊", "🤗", "😄", "🥳", "😁"},
	},
	models.TriggerOrderFulfilled: {
		{"📦", "🚚", "🚀", "✈️", "📬"},
		{"✅", "🎯", "💫", "⚡", "🔔"},
		{"😊", "🤗", "🥳", "🎉", "👏"},
	},
	models.TriggerOrderCancelled: {
		{"💙", "🤍", "🙏", "💜", "🫂"},
		{"📩", "💬", "📱", "🔔", "📨"},
	},
	models.TriggerAbandonedCart: {
		{"🛒", "🛍️", "🏷️", "💡", "👀"},
		{"💌", "📩", "💬", "🔔", "📱"},
		{"🌟", "⭐", "💫", "✨", "🎁"},
	},
}

// fallbackEmojiSets used when trigger is not specified (manual sends).
var fallbackEmojiSets = [][]string{
	{"💬", "📱", "🔔", "📩", "💌"},
	{"✅", "👍", "🙌", "💪", "🤝"},
	{"🌟", "⭐", "💫", "✨", "🎉"},
}

type emojiPosition int

const (
	posEnd   emojiPosition = iota
	posStart               //nolint
	posBoth
)

// RandomizeMessage adds a random context-neutral emoji and randomises placement.
// Use RandomizeMessageForTrigger when the trigger type is known.
func RandomizeMessage(text string) string {
	return randomize(text, fallbackEmojiSets)
}

// RandomizeMessageForTrigger picks emojis appropriate for the given trigger.
func RandomizeMessageForTrigger(text string, trigger models.TriggerType) string {
	sets, ok := triggerEmojiSets[trigger]
	if !ok {
		sets = fallbackEmojiSets
	}
	return randomize(text, sets)
}

func randomize(text string, sets [][]string) string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	pos := emojiPosition(r.Intn(3))
	set1 := sets[r.Intn(len(sets))]
	e1 := set1[r.Intn(len(set1))]
	body := strings.TrimSpace(text)

	switch pos {
	case posStart:
		return fmt.Sprintf("%s %s", e1, body)
	case posBoth:
		// Pick second emoji from a different set.
		idx2 := (r.Intn(len(sets)-1) + 1 + r.Intn(len(sets))) % len(sets)
		e2 := sets[idx2][r.Intn(len(sets[idx2]))]
		return fmt.Sprintf("%s %s %s", e1, body, e2)
	default:
		return fmt.Sprintf("%s %s", body, e1)
	}
}

// jitterBands are non-uniform so timing patterns are not obvious.
var jitterBands = [][2]int{
	{3, 12}, {8, 25}, {5, 18}, {15, 45},
	{2, 9}, {20, 60}, {7, 30}, {12, 40},
}

// JitterDelay returns base delay + random extra seconds.
// Even a 0-minute base gets a small jitter so messages never fire simultaneously.
func JitterDelay(baseMinutes int) time.Duration {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	band := jitterBands[r.Intn(len(jitterBands))]
	extra := band[0] + r.Intn(band[1]-band[0]+1)
	return time.Duration(baseMinutes)*time.Minute + time.Duration(extra)*time.Second
}
