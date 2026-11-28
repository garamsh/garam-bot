package workspaceaccess

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

const (
	// sweepGrace is how long a workspace's Bot must have been found gone before the sweep deletes it.
	sweepGrace = 10 * time.Minute
	// defaultSweepEvery is how often the sweep lists the providers' workspaces.
	defaultSweepEvery = time.Minute
)

// sweeper holds when the sweep first found each workspace's Bot gone. It is held in memory alone, so
// a restart starts every grace again.
type sweeper struct {
	// running serialises the passes, so a finding is never read and written by two at once.
	running sync.Mutex
	found   map[swept]time.Time
}

// swept is a workspace, by its place's provider and its name.
type swept struct {
	provider string
	name     workspace.Name
}

func newSweeper() *sweeper { return &sweeper{found: map[swept]time.Time{}} }

// owner is the Bot and attachment a workspace's labels name.
type owner struct {
	bot        Bot
	uid        string
	attachment string
}

// sweep deletes each of this release's workspaces whose Bot the API server has answered gone, or made
// again, for sweepGrace, with the clock moved by skew. A workspace whose labels it cannot read, whose
// labels do not name it, or whose Bot's namespace is no longer watched or claimed is never touched
// (ADR 0099 decision 6).
func (s *service) sweep(ctx context.Context, skew time.Duration) {
	s.sweeper.running.Lock()
	defer s.sweeper.running.Unlock()
	now := s.cfg.Now().Add(skew)
	held := map[swept]bool{}
	for _, provider := range slices.Sorted(maps.Keys(s.cfg.Places)) {
		place := s.cfg.Places[provider]
		caps, err := place.Provider.Capabilities(ctx)
		if err != nil || !caps.Labels {
			continue
		}
		all, err := place.Provider.List(ctx)
		if err != nil {
			s.cfg.Log.Warn("the sweep cannot list a provider's workspaces", "provider", provider, "error", err)
			continue
		}
		for _, ws := range all {
			key := swept{provider: provider, name: ws.Name}
			o, ok := s.ownerOf(ws)
			if !ok || !s.stillServed(ctx, o.bot) {
				continue
			}
			gone, err := s.gone(ctx, o)
			if err != nil {
				// No answer is no evidence: the finding stands, and nothing is deleted.
				if _, found := s.sweeper.found[key]; found {
					held[key] = true
				}
				continue
			}
			if !gone {
				continue
			}
			held[key] = true
			first, found := s.sweeper.found[key]
			if !found {
				s.sweeper.found[key] = now
				continue
			}
			if now.Sub(first) < sweepGrace {
				continue
			}
			log := s.cfg.Log.With("namespace", o.bot.Namespace, "bot", o.bot.Name, "attachment", o.attachment)
			if err := place.Provider.Delete(ctx, ws.Name); err != nil {
				log.Warn("the sweep cannot delete a gone Bot's workspace", "error", err)
				continue
			}
			delete(held, key)
			log.Info("the sweep deleted a gone Bot's workspace")
		}
	}
	maps.DeleteFunc(s.sweeper.found, func(key swept, _ time.Time) bool { return !held[key] })
}

// ownerOf answers the owner a workspace's labels name, where the workspace is this release's: its name
// begins with the release's prefix, its labels name the release, and the name is the one the labels'
// Bot and attachment are given.
func (s *service) ownerOf(ws workspace.Workspace) (owner, bool) {
	labels := ws.Spec.Labels
	o := owner{
		bot: Bot{Namespace: labels[LabelBotNamespace], Name: labels[LabelBotName]},
		uid: labels[LabelBotUID], attachment: labels[LabelAttachment],
	}
	release := s.cfg.Release
	switch {
	case !strings.HasPrefix(string(ws.Name), release.Prefix()),
		labels[LabelReleaseNamespace] != release.Namespace || labels[LabelReleaseName] != release.Name,
		o.bot.Namespace == "" || o.bot.Name == "" || o.uid == "" || o.attachment == "",
		workspaceName(release, o.bot, o.uid, o.attachment) != ws.Name:
		return owner{}, false
	}
	return o, true
}

// stillServed answers whether the Bot's namespace is still watched and claimed for this release. A
// read that fails is answered false, so nothing is touched on it.
func (s *service) stillServed(ctx context.Context, bot Bot) bool {
	if !s.namespaces[bot.Namespace] {
		return false
	}
	served, err := s.cfg.Bots.Served(ctx, bot.Namespace)
	return err == nil && served
}

// gone answers whether the API server holds no Bot of the owner's name, or one made again under it.
func (s *service) gone(ctx context.Context, o owner) (bool, error) {
	uid, exists, err := s.cfg.LiveBots.UID(ctx, o.bot)
	if err != nil {
		return false, err
	}
	return !exists || uid != o.uid, nil
}
