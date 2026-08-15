package digest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"golang.org/x/text/unicode/norm"
)

type Briefing string

const (
	PreMarket Briefing = "pre_market"
	Closing   Briefing = "closing"
	USPreview Briefing = "us_preview"
	// NewsAggregate 新闻聚合简报(早报/晚报):digest 新闻条目的统一去向,
	// 由 news-aggregate-* 源租用。2026-08-02 起 digest 不再进 us_preview。
	NewsAggregate Briefing = "news_aggregate"
)

func (b Briefing) Valid() bool {
	return b == PreMarket || b == Closing || b == USPreview || b == NewsAggregate
}

type State string

const (
	Pending    State = "pending"
	Leased     State = "leased"
	Delivering State = "delivering"
	Acked      State = "acked"
	Expired    State = "expired"
)

func (s State) Valid() bool {
	return s == Pending || s == Leased || s == Delivering || s == Acked || s == Expired
}

const SchemaVersion = 4

type Item struct {
	ID             string         `json:"id"`
	SchemaVersion  int            `json:"schema_version,omitempty"`
	Source         string         `json:"source"`
	Briefing       Briefing       `json:"briefing"`
	Message        *model.Message `json:"message"`
	State          State          `json:"state"`
	Priority       int            `json:"priority,omitempty"`
	TopicKey       string         `json:"topic_key,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	ExpiresAt      time.Time      `json:"expires_at,omitempty"`
	LeaseID        string         `json:"lease_id,omitempty"`
	LeaseUntil     time.Time      `json:"lease_until,omitempty"`
	DeliveryID     string         `json:"delivery_id,omitempty"`
	AcknowledgedAt time.Time      `json:"acknowledged_at,omitempty"`
	TerminalAt     time.Time      `json:"terminal_at,omitempty"`
	TerminalReason string         `json:"terminal_reason,omitempty"`
}

type Lease struct {
	ID       string
	Briefing Briefing
	Items    []Item
	Topics   []Topic
}

// Topic is a deterministic group leased as one indivisible unit.
type Topic struct {
	Key      string
	Priority int
	Items    []Item
}

type RouteLane string

const (
	RouteDirect RouteLane = "direct"
	RouteDigest RouteLane = "digest"
	RouteSilent RouteLane = "silent"
)

func (l RouteLane) Valid() bool {
	return l == RouteDirect || l == RouteDigest || l == RouteSilent
}

type RouteRequest struct {
	Source    string
	MessageID string
	Lane      RouteLane
	Briefing  Briefing
	MaxPerDay int
	Priority  int
	Critical  bool
	Channels  []string
	Payload   *model.Message
}

type DirectClaim struct {
	Source    string
	MessageID string
	Token     string
	Message   *model.Message
	Channels  []string
}

type RouteAdmission struct {
	Admitted  bool
	Existing  bool
	Lane      RouteLane
	Used      int
	Limit     int
	Completed bool
	Channels  []string
}

type DeliveryState string

const (
	DeliveryPending   DeliveryState = "pending"
	DeliveryDelivered DeliveryState = "delivered"
	DeliveryExpired   DeliveryState = "expired"
)

func (s DeliveryState) Valid() bool {
	return s == DeliveryPending || s == DeliveryDelivered || s == DeliveryExpired
}

var (
	ErrDeliveryNotFound     = errors.New("digest delivery not found")
	ErrDeliveryTerminal     = errors.New("digest delivery is terminal")
	ErrDeliveryClaimActive  = errors.New("digest delivery has an active claim")
	ErrDeliveryNotRetryable = errors.New("digest delivery has nothing retryable")
	ErrDeliveryDeadline     = errors.New("digest delivery deadline elapsed")
)

// SinkCheckpoint is durable per-sink delivery progress.
type SinkCheckpoint struct {
	Sink           string               `json:"sink"`
	Delivered      bool                 `json:"delivered"`
	DeliveredAt    time.Time            `json:"delivered_at,omitempty"`
	LastFailedAt   time.Time            `json:"last_failed_at,omitempty"`
	Attempts       int                  `json:"attempts,omitempty"`
	FailedAttempts int                  `json:"failed_attempts,omitempty"`
	AttemptToken   string               `json:"attempt_token,omitempty"`
	ClaimedUntil   time.Time            `json:"claimed_until,omitempty"`
	NextAttemptAt  time.Time            `json:"next_attempt_at,omitempty"`
	LastError      string               `json:"last_error,omitempty"`
	Outcomes       []SinkAttemptOutcome `json:"outcomes,omitempty"`
}

// SinkAttemptOutcome preserves the canonical completion order needed to
// reconstruct per-sink failure streaks across interleaved deliveries.
type SinkAttemptOutcome struct {
	At      time.Time `json:"at"`
	Order   uint64    `json:"order,omitempty"`
	Success bool      `json:"success"`
}

// Delivery stores the exact rendered payload and all required sink checkpoints.
type Delivery struct {
	ID             string                    `json:"id"`
	SchemaVersion  int                       `json:"schema_version"`
	LeaseID        string                    `json:"lease_id"`
	Briefing       Briefing                  `json:"briefing"`
	ItemIDs        []string                  `json:"item_ids"`
	Message        *model.Message            `json:"message"`
	PayloadHash    string                    `json:"payload_hash"`
	RequiredSinks  []string                  `json:"required_sinks"`
	Sinks          map[string]SinkCheckpoint `json:"sinks"`
	State          DeliveryState             `json:"state"`
	CreatedAt      time.Time                 `json:"created_at"`
	UpdatedAt      time.Time                 `json:"updated_at"`
	Deadline       time.Time                 `json:"deadline"`
	TerminalAt     time.Time                 `json:"terminal_at,omitempty"`
	TerminalReason string                    `json:"terminal_reason,omitempty"`
}

// Attempt is a fenced unit of sink work returned to a delivery worker.
type Attempt struct {
	DeliveryID   string         `json:"delivery_id"`
	Sink         string         `json:"sink"`
	Token        string         `json:"token"`
	Number       int            `json:"number"`
	ClaimedUntil time.Time      `json:"claimed_until"`
	Message      *model.Message `json:"message"`
}

type ItemFilter struct {
	Briefing Briefing
	State    State
	Limit    int
}

type DeliveryFilter struct {
	State DeliveryState
	Sink  string
	Limit int
}

type Stats struct {
	Items                    map[State]int
	Deliveries               map[DeliveryState]int
	Sinks                    map[string]SinkStats `json:"sinks,omitempty"`
	OldestPendingItemAt      time.Time            `json:"oldest_pending_item_at,omitempty"`
	OldestLeasedItemAt       time.Time            `json:"oldest_leased_item_at,omitempty"`
	OldestDeliveringItemAt   time.Time            `json:"oldest_delivering_item_at,omitempty"`
	OldestPendingDeliveryAt  time.Time            `json:"oldest_pending_delivery_at,omitempty"`
	LatestItemExpiryAt       time.Time            `json:"latest_item_expiry_at,omitempty"`
	LatestDeadlineExpiryAt   time.Time            `json:"latest_deadline_expiry_at,omitempty"`
	LatestMaxAttemptExpiryAt time.Time            `json:"latest_max_attempt_expiry_at,omitempty"`
}

// SinkStats is reconstructed from canonical delivery checkpoints. It is
// intentionally low-cardinality: only configured required sink names appear.
type SinkStats struct {
	Pending             int       `json:"pending"`
	OldestPendingAt     time.Time `json:"oldest_pending_at,omitempty"`
	LastDeliveredAt     time.Time `json:"last_delivered_at,omitempty"`
	LastFailedAt        time.Time `json:"last_failed_at,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
}

type ExpiryResult struct {
	Items                int
	Deliveries           int
	DeadlineDeliveries   int
	MaxAttemptDeliveries int
}

type Store interface {
	Enqueue(context.Context, string, Briefing, *model.Message) error
	Lease(context.Context, Briefing, int, time.Duration) (*Lease, error)
	// Ack is the legacy all-or-nothing acknowledgement path. New workers should
	// use PrepareDelivery, ClaimDueAttempts, and CompleteAttempt on DigestStore.
	Ack(context.Context, string) error
}

// RoutedStore is optional so existing Store fakes remain source-compatible.
type RoutedStore interface {
	LookupDirect(context.Context, RouteRequest) (RouteAdmission, error)
	AdmitDirect(context.Context, RouteRequest) (RouteAdmission, error)
	ClaimDirect(context.Context, string, string, time.Duration) (*DirectClaim, error)
	ClaimPendingDirect(context.Context, int, time.Duration) ([]DirectClaim, error)
	CompleteDirectChannel(context.Context, string, string, string, string) error
	FailDirectChannel(context.Context, string, string, string, string, string, bool) error
	ReleaseDirect(context.Context, string, string, string) error
	EnqueueRoutedDigest(context.Context, RouteRequest, *model.Message) (RouteAdmission, error)
}

// TopicStore is the optional topic-aware leasing surface.
type TopicStore interface {
	LeaseTopics(context.Context, Briefing, int, time.Duration) (*Lease, error)
}

// AdminStore is the intentionally narrow operational surface exposed to the API.
type AdminStore interface {
	Stats(context.Context) (Stats, error)
	ListItems(context.Context, ItemFilter) ([]Item, error)
	ListDeliveries(context.Context, DeliveryFilter) ([]Delivery, error)
	RetryDelivery(context.Context, string) error
}

// Cluster is a deterministic group of related digest items.
type Cluster struct {
	Key   string
	Items []Item
}

// ClusterItems groups items by their first normalized title token. Both clusters
// and their contents have stable ordering, independent of Bolt cursor order.
func ClusterItems(items []Item) []Cluster {
	groups := make(map[string][]Item)
	for _, item := range items {
		key := clusterKey(item)
		groups[key] = append(groups[key], item)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]Cluster, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].CreatedAt.Equal(group[j].CreatedAt) {
				return group[i].ID < group[j].ID
			}
			return group[i].CreatedAt.Before(group[j].CreatedAt)
		})
		out = append(out, Cluster{Key: key, Items: group})
	}
	return out
}

func clusterKey(item Item) string {
	if item.TopicKey != "" {
		return item.TopicKey
	}
	if item.Message == nil {
		return item.ID
	}
	title := strings.ToLower(strings.TrimSpace(item.Message.Title))
	fields := strings.FieldsFunc(title, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') &&
			!(r >= '\u4e00' && r <= '\u9fff')
	})
	if len(fields) == 0 {
		return item.ID
	}
	return fields[0]
}

// TopicKey returns a deterministic conservative v1 identity. URL-backed items
// use only the normalized host and stable path. Items without a usable URL use
// an exact normalized title and a six-hour bucket to avoid indefinite merging.
func TopicKey(briefing Briefing, msg *model.Message, at time.Time) string {
	identity := ""
	if msg != nil {
		if parsed, err := url.Parse(strings.TrimSpace(msg.Link)); err == nil &&
			(parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" {
			host := strings.ToLower(parsed.Hostname())
			stablePath := path.Clean("/" + strings.TrimSpace(parsed.EscapedPath()))
			if stablePath == "/." {
				stablePath = "/"
			}
			// A site root is not an article identity and would over-merge every
			// root-linked item from the source.
			if stablePath != "/" {
				identity = "url\x00" + host + "\x00" + stablePath
			}
		}
		if identity == "" {
			title := normalizeTopicTitle(msg.Title)
			if title == "" {
				title = normalizeTopicTitle(msg.Content)
			}
			bucket := at.UTC().Unix() / int64(6*time.Hour/time.Second)
			identity = fmt.Sprintf("title\x00%s\x00%d", title, bucket)
		}
	}
	if identity == "" {
		bucket := at.UTC().Unix() / int64(6*time.Hour/time.Second)
		identity = fmt.Sprintf("empty\x00%d", bucket)
	}
	sum := sha256.Sum256([]byte("topic:v1\x00" + string(briefing) + "\x00" + identity))
	return "topic:v1:" + hex.EncodeToString(sum[:])
}

func normalizeTopicTitle(value string) string {
	value = norm.NFD.String(strings.ToLower(strings.TrimSpace(value)))
	fields := strings.FieldsFunc(value, func(r rune) bool {
		if unicode.Is(unicode.Mn, r) {
			return false
		}
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
			(r >= '\u4e00' && r <= '\u9fff'))
	})
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, strings.Join(fields, " "))
}

// LeaseTopicKey returns the ephemeral identity used to group items in a lease.
// An exact normalized title aliases publisher-specific URLs without changing
// the durable TopicKey stored on each item.
func LeaseTopicKey(briefing Briefing, msg *model.Message, durableTopicKey string) string {
	if msg == nil {
		return durableTopicKey
	}
	title := normalizeTopicTitle(msg.Title)
	if title == "" {
		return durableTopicKey
	}
	sum := sha256.Sum256([]byte("lease-topic:v1\x00" + string(briefing) + "\x00" + title))
	return "lease-topic:v1:" + hex.EncodeToString(sum[:])
}

type topicUsefulness struct {
	hasContent      bool
	contentLength   int
	hasShortContent bool
	shortLength     int
	mediaCount      int
	hasLink         bool
}

func itemTopicUsefulness(item Item) topicUsefulness {
	if item.Message == nil {
		return topicUsefulness{}
	}
	content := strings.TrimSpace(item.Message.Content)
	shortContent := strings.TrimSpace(item.Message.ShortContent)
	mediaCount := len(item.Message.ImageURLs)
	if strings.TrimSpace(item.Message.ImageURL) != "" {
		mediaCount++
	}
	if strings.TrimSpace(item.Message.VideoURL) != "" {
		mediaCount++
	}
	return topicUsefulness{
		hasContent:      content != "",
		contentLength:   len([]rune(content)),
		hasShortContent: shortContent != "",
		shortLength:     len([]rune(shortContent)),
		mediaCount:      mediaCount,
		hasLink:         strings.TrimSpace(item.Message.Link) != "",
	}
}

func moreUsefulTopicItem(left, right Item) bool {
	l, r := itemTopicUsefulness(left), itemTopicUsefulness(right)
	if l.hasContent != r.hasContent {
		return l.hasContent
	}
	if l.contentLength != r.contentLength {
		return l.contentLength > r.contentLength
	}
	if l.hasShortContent != r.hasShortContent {
		return l.hasShortContent
	}
	if l.shortLength != r.shortLength {
		return l.shortLength > r.shortLength
	}
	if l.mediaCount != r.mediaCount {
		return l.mediaCount > r.mediaCount
	}
	if l.hasLink != r.hasLink {
		return l.hasLink
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.After(right.CreatedAt)
	}
	return left.ID < right.ID
}

// RenderTopics renders one bounded line per topic with deterministic multi-source attribution.
func RenderTopics(topics []Topic, limit int) string {
	if limit <= 0 || limit > 12 {
		limit = 12
	}
	ordered := append([]Topic(nil), topics...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority > ordered[j].Priority
		}
		return ordered[i].Key < ordered[j].Key
	})
	lines := make([]string, 0, min(limit, len(ordered)))
	for _, topic := range ordered {
		if len(lines) >= limit {
			break
		}
		items := append([]Item(nil), topic.Items...)
		sort.Slice(items, func(i, j int) bool { return moreUsefulTopicItem(items[i], items[j]) })
		var title string
		if len(items) > 0 && items[0].Message != nil {
			title = strings.TrimSpace(items[0].Message.Title)
			if title == "" {
				title = strings.TrimSpace(items[0].Message.Content)
			}
		}
		sources := make(map[string]struct{})
		for _, item := range items {
			if item.Source != "" {
				sources[item.Source] = struct{}{}
			}
		}
		if title == "" {
			continue
		}
		if runes := []rune(title); len(runes) > 100 {
			title = string(runes[:100]) + "…"
		}
		names := make([]string, 0, len(sources))
		for source := range sources {
			if runes := []rune(source); len(runes) > 24 {
				source = string(runes[:24]) + "…"
			}
			names = append(names, source)
		}
		sort.Strings(names)
		attribution := strings.Join(names, "、")
		if len(names) > 4 {
			attribution = strings.Join(names[:4], "、") + fmt.Sprintf("等%d源", len(names))
		}
		lines = append(lines, fmt.Sprintf("- %s（%s）", title, attribution))
	}
	return strings.Join(lines, "\n")
}

// Render returns a bounded, deterministic markdown summary.
func Render(items []Item, limit int) string {
	if limit <= 0 {
		limit = 12
	}
	var lines []string
	for _, cluster := range ClusterItems(items) {
		for _, item := range cluster.Items {
			if len(lines) >= limit {
				break
			}
			if item.Message == nil {
				continue
			}
			title := strings.TrimSpace(item.Message.Title)
			if title == "" {
				title = strings.TrimSpace(item.Message.Content)
			}
			if title == "" {
				continue
			}
			if len([]rune(title)) > 100 {
				title = string([]rune(title)[:100]) + "…"
			}
			lines = append(lines, fmt.Sprintf("- %s（%s）", title, item.Source))
		}
		if len(lines) >= limit {
			break
		}
	}
	return strings.Join(lines, "\n")
}
