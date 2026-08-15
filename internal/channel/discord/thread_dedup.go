package discord

import (
	"crypto/sha256"
	"encoding/hex"
	"html"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-ego/gse"
	"github.com/kljensen/snowball"
)

// ThreadDedup Thread 内去重
type ThreadDedup struct {
	recent    map[string]map[string][]*DedupEntry
	seg       gse.Segmenter
	window    time.Duration
	threshold float64
	mu        sync.RWMutex
}

type DedupEntry struct {
	Fingerprint string
	Time        time.Time
	MsgID       string
}

func NewThreadDedup(window time.Duration, threshold float64) *ThreadDedup {
	d := &ThreadDedup{
		recent:    make(map[string]map[string][]*DedupEntry),
		window:    window,
		threshold: threshold,
	}
	d.seg.LoadDict("zh")
	return d
}

func (d *ThreadDedup) ShouldSend(sourceName, threadID, msgID string, title, content string) bool {
	fingerprint := d.fingerprint(title, content)

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.recent[sourceName]; !ok {
		d.recent[sourceName] = make(map[string][]*DedupEntry)
	}
	entries := d.recent[sourceName][threadID]
	now := time.Now()

	var valid []*DedupEntry
	for _, e := range entries {
		if now.Sub(e.Time) <= d.window {
			valid = append(valid, e)
		}
	}

	for _, e := range valid {
		if e.MsgID == msgID {
			return false
		}
		if e.Fingerprint == fingerprint {
			return false
		}
	}

	valid = append(valid, &DedupEntry{
		Fingerprint: fingerprint,
		Time:        now,
		MsgID:       msgID,
	})

	if len(valid) > 100 {
		valid = valid[len(valid)-100:]
	}

	d.recent[sourceName][threadID] = valid
	return true
}

func (d *ThreadDedup) fingerprint(title, content string) string {
	text := html.UnescapeString(title + " " + content)
	cleaned := regexp.MustCompile(`[^\p{L}\p{N}\s]`).ReplaceAllString(text, "")
	cleaned = strings.ToLower(strings.TrimSpace(cleaned))

	segments := d.seg.Cut(cleaned, true)
	var words []string
	for _, word := range segments {
		word = strings.TrimSpace(word)
		if len([]rune(word)) < 2 {
			continue
		}
		if d.isStopWord(word) {
			continue
		}
		if d.isEnglish(word) {
			stemmed, _ := snowball.Stem(word, "english", true)
			word = stemmed
		}
		words = append(words, word)
	}

	data := strings.Join(words, " ")
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

func (d *ThreadDedup) isStopWord(word string) bool {
	stops := map[string]bool{
		"的": true, "是": true, "在": true, "了": true, "和": true,
		"a": true, "the": true, "is": true, "it": true, "this": true,
	}
	return stops[strings.ToLower(word)]
}

func (d *ThreadDedup) isEnglish(word string) bool {
	for _, r := range word {
		if !unicode.IsLetter(r) || r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

func (d *ThreadDedup) Cleanup() {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	for src, threads := range d.recent {
		for threadID, entries := range threads {
			var valid []*DedupEntry
			for _, e := range entries {
				if now.Sub(e.Time) <= d.window {
					valid = append(valid, e)
				}
			}
			if len(valid) == 0 {
				delete(threads, threadID)
			} else {
				threads[threadID] = valid
			}
		}
		if len(threads) == 0 {
			delete(d.recent, src)
		}
	}
}
