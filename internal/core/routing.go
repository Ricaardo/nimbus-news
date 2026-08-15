package core

import (
	"context"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/config"
	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/filter"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

type typedRouteDecision struct {
	lane     digest.RouteLane
	band     *config.RouteBand
	eval     filter.EvalResult
	critical bool
}

func cloneMessageMetadata(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return make(map[string]interface{})
	}
	cloned := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func (e *NewsEngine) routeTypedMessages(ctx context.Context, sourceName string, info newsSourceInfo, messages []*model.Message) bool {
	routedStore, ok := e.digestStore.(digest.RoutedStore)
	if !ok {
		slog.Error("typed routing unavailable", "source", sourceName)
		return false
	}
	for _, msg := range messages {
		msg.SetMetadata("typed_routing", "1")
		decision := e.decideTypedRoute(ctx, sourceName, info.routing, msg)
		setTypedEvalMetadata(msg, decision.eval)
		if decision.critical {
			msg.SetMetadata("typed_routing_critical", "1")
		}

		if decision.lane == digest.RouteDirect {
			passed, prepared := e.prepareTypedDirect(ctx, sourceName, info, msg)
			if !prepared {
				continue
			}
			request := routeRequest(sourceName, msg, decision, digest.RouteDirect)
			request.Channels = append([]string(nil), passed...)
			request.Payload = msg
			admission, err := routedStore.AdmitDirect(ctx, request)
			if err != nil {
				slog.Error("direct route admission failed", "source", sourceName, "error", err)
				return false
			}
			if !admission.Admitted {
				if digestBandEligible(info.routing.Digest, decision.eval) {
					decision.lane = digest.RouteDigest
					decision.band = info.routing.Digest
					msg.SetMetadata("routing_lane", string(decision.lane))
					if !e.admitTypedDigest(ctx, routedStore, sourceName, decision, msg) {
						return false
					}
				} else {
					decision.lane = digest.RouteSilent
				}
				continue
			}
			msg.SetMetadata("routing_lane", string(digest.RouteDirect))
			claim, err := routedStore.ClaimDirect(ctx, sourceName, msg.ID, time.Minute)
			if err != nil {
				slog.Error("claim direct route failed", "source", sourceName, "error", err)
				return false
			}
			if claim != nil {
				e.runDirectClaim(ctx, routedStore, *claim)
			}
			continue
		}

		msg.SetMetadata("routing_lane", string(decision.lane))
		switch decision.lane {
		case digest.RouteDigest:
			if !e.enqueueTypedDigest(ctx, routedStore, sourceName, decision, msg) {
				return false
			}
		}
	}
	return true
}

func (e *NewsEngine) decideTypedRoute(ctx context.Context, sourceName string, routing *config.SourceRoutingConfig, msg *model.Message) typedRouteDecision {
	if routing.Critical || matchesCriticalKeyword(routing.CriticalKeywords, msg) {
		return typedRouteDecision{lane: digest.RouteDirect, band: routing.Direct, critical: true}
	}
	if routing.AIPolicy != config.AIPolicyRequired {
		return defaultTypedRoute(routing, filter.EvalResult{})
	}

	result := e.evaluateTyped(ctx, sourceName, msg)
	if result.Status != filter.EvalSuccess {
		if routing.OnAIFailure == config.RouteDefaultDigest {
			return typedRouteDecision{lane: digest.RouteDigest, band: routing.Digest, eval: result}
		}
		return typedRouteDecision{lane: digest.RouteSilent, eval: result}
	}
	if result.Blocked {
		return typedRouteDecision{lane: digest.RouteSilent, eval: result}
	}
	if bandMatches(routing.Direct, result.Score) {
		return typedRouteDecision{lane: digest.RouteDirect, band: routing.Direct, eval: result}
	}
	if bandMatches(routing.Digest, result.Score) {
		return typedRouteDecision{lane: digest.RouteDigest, band: routing.Digest, eval: result}
	}
	return defaultTypedRoute(routing, result)
}

func (e *NewsEngine) evaluateTyped(ctx context.Context, sourceName string, msg *model.Message) filter.EvalResult {
	if e.filters != nil {
		for _, candidate := range e.filters.GetFilters() {
			if evaluator, ok := candidate.(*filter.AIFilter); ok {
				return evaluator.EvaluateTyped(ctx, sourceName, msg)
			}
		}
	}
	return filter.EvalResult{Status: filter.EvalUnavailable, Reason: "AI评估器不可用"}
}

func defaultTypedRoute(routing *config.SourceRoutingConfig, result filter.EvalResult) typedRouteDecision {
	if routing.Default == config.RouteDefaultDirect {
		return typedRouteDecision{lane: digest.RouteDirect, band: routing.Direct, eval: result}
	}
	if routing.Default == config.RouteDefaultDigest {
		return typedRouteDecision{lane: digest.RouteDigest, band: routing.Digest, eval: result}
	}
	return typedRouteDecision{lane: digest.RouteSilent, eval: result}
}

func (e *NewsEngine) prepareTypedDirect(ctx context.Context, sourceName string, info newsSourceInfo, msg *model.Message) ([]string, bool) {
	channels := e.withMirror(info.channels)
	passed := make([]string, 0, len(channels))
	for _, channel := range channels {
		if e.filters == nil || !e.filters.ShouldFilter(sourceName, channel, msg) {
			passed = append(passed, channel)
		}
	}
	if len(passed) == 0 {
		return nil, false
	}
	if e.enhancer != nil {
		e.enhancer.EnhanceBatch(ctx, []*model.Message{msg}, 1)
	}
	if msg.GetStringMetadata("drop") == "1" || e.router == nil {
		return nil, false
	}
	return passed, true
}

func (e *NewsEngine) deliverDirectClaim(ctx context.Context, routedStore digest.RoutedStore, claim digest.DirectClaim) bool {
	if e.router == nil || claim.Message == nil {
		_ = routedStore.ReleaseDirect(ctx, claim.Source, claim.MessageID, claim.Token)
		return false
	}
	dispatchCtx, cancelDispatch := context.WithTimeout(ctx, 20*time.Second)
	defer cancelDispatch()
	ok := true
	for _, channel := range claim.Channels {
		if manager := e.router.GetChannelManager(); manager == nil || !manager.Has(channel) {
			slog.Error("typed direct channel unavailable", "source", claim.Source, "channel", channel)
			checkpointCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = routedStore.FailDirectChannel(checkpointCtx, claim.Source, claim.MessageID, claim.Token, channel, "channel unavailable", true)
			cancel()
			ok = false
			continue
		}
		if err := e.router.DispatchNews(dispatchCtx, claim.Message, []string{channel}); err != nil {
			slog.Error("typed direct dispatch error", "source", claim.Source, "channel", channel, "error", err)
			checkpointCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = routedStore.FailDirectChannel(checkpointCtx, claim.Source, claim.MessageID, claim.Token, channel, err.Error(), false)
			cancel()
			ok = false
			continue
		}
		// External send and the following Bolt checkpoint cannot be atomic. A
		// process crash in this window may resend the channel on recovery; direct
		// delivery therefore remains intentionally at-least-once.
		checkpointCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := routedStore.CompleteDirectChannel(checkpointCtx, claim.Source, claim.MessageID, claim.Token, channel)
		cancel()
		if err != nil {
			slog.Error("complete direct channel failed", "source", claim.Source, "channel", channel, "error", err)
			ok = false
		}
	}
	releaseCtx, cancelRelease := context.WithTimeout(context.Background(), 2*time.Second)
	err := routedStore.ReleaseDirect(releaseCtx, claim.Source, claim.MessageID, claim.Token)
	cancelRelease()
	if err != nil {
		slog.Error("release direct claim failed", "source", claim.Source, "error", err)
		return false
	}
	return ok
}

func (e *NewsEngine) enqueueTypedDigest(ctx context.Context, routedStore digest.RoutedStore, sourceName string, decision typedRouteDecision, msg *model.Message) bool {
	if e.filters != nil && e.filters.ShouldFilter(sourceName, filter.DigestSinkName, msg) {
		return true
	}
	if e.enhancer != nil {
		e.enhancer.EnhanceBatch(ctx, []*model.Message{msg}, 1)
	}
	if msg.GetStringMetadata("drop") == "1" {
		return true
	}
	return e.admitTypedDigest(ctx, routedStore, sourceName, decision, msg)
}

func (e *NewsEngine) admitTypedDigest(ctx context.Context, routedStore digest.RoutedStore, sourceName string, decision typedRouteDecision, msg *model.Message) bool {
	admission, err := routedStore.EnqueueRoutedDigest(ctx, routeRequest(sourceName, msg, decision, digest.RouteDigest), msg)
	if err != nil {
		slog.Error("digest route admission failed", "source", sourceName, "error", err)
		return false
	}
	if !admission.Admitted {
		msg.SetMetadata("routing_lane", string(digest.RouteSilent))
		// 可观测性:高分新闻被每日配额拦截时留痕,便于发现配额过早耗尽。
		slog.Info("digest quota exhausted", "source", sourceName, "lane", string(digest.RouteDigest),
			"title", truncateTitle(msg.Title, 60), "used", admission.Used, "limit", admission.Limit,
			"completed", admission.Completed)
	}
	return true
}

func truncateTitle(title string, maxRunes int) string {
	runes := []rune(title)
	if len(runes) <= maxRunes {
		return title
	}
	return string(runes[:maxRunes]) + "…"
}

func routeRequest(sourceName string, msg *model.Message, decision typedRouteDecision, lane digest.RouteLane) digest.RouteRequest {
	request := digest.RouteRequest{Source: sourceName, MessageID: msg.ID, Lane: lane, Critical: decision.critical}
	if decision.band != nil {
		request.MaxPerDay = decision.band.MaxPerDay
		request.Priority = routedPriority(decision.band.Priority, decision.eval)
		request.Briefing = digest.Briefing(decision.band.BriefingTarget)
	}
	return request
}

func setTypedEvalMetadata(msg *model.Message, result filter.EvalResult) {
	if result.Status == "" {
		return
	}
	msg.SetMetadata("ai_eval_status", string(result.Status))
	msg.SetMetadata("ai_reason", result.Reason)
	if result.Status == filter.EvalSuccess {
		msg.SetMetadata("ai_score", result.Score)
		msg.SetMetadata("ai_category", result.Category)
	}
}

func matchesCriticalKeyword(keywords []string, msg *model.Message) bool {
	text := normalizedRouteText(msg.Title + " " + msg.Content)
	for _, keyword := range keywords {
		if normalized := normalizedRouteText(keyword); normalized != "" && routePhraseMatches(text, normalized) {
			return true
		}
	}
	return false
}

func routePhraseMatches(text, phrase string) bool {
	if !isASCIIWordPhrase(phrase) {
		return strings.Contains(text, phrase)
	}
	for offset := 0; offset <= len(text)-len(phrase); {
		index := strings.Index(text[offset:], phrase)
		if index < 0 {
			return false
		}
		index += offset
		leftOK := index == 0 || !isASCIIWordByte(text[index-1])
		right := index + len(phrase)
		rightOK := right == len(text) || !isASCIIWordByte(text[right])
		if leftOK && rightOK {
			return true
		}
		offset = index + 1
	}
	return false
}

func isASCIIWordPhrase(value string) bool {
	hasWord := false
	for i := 0; i < len(value); i++ {
		if isASCIIWordByte(value[i]) {
			hasWord = true
			continue
		}
		if value[i] != ' ' {
			return false
		}
	}
	return hasWord
}

func isASCIIWordByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_'
}

func normalizedRouteText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func bandMatches(band *config.RouteBand, score float64) bool {
	return band != nil && band.MinScore != nil && score >= *band.MinScore
}

func digestBandEligible(band *config.RouteBand, result filter.EvalResult) bool {
	return result.Status == filter.EvalSuccess && bandMatches(band, result.Score)
}

func routedPriority(base int, result filter.EvalResult) int {
	if result.Status == filter.EvalSuccess {
		base += int(math.Round(result.Score)) - 5
	}
	return max(-100, min(100, base))
}
