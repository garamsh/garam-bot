package reference

import (
	"crypto/rand"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// The place's limits (LIMIT-1, LIMIT-2) and the server's dial cap (DIAL-3).
const (
	botBurst        = 20
	botEvery        = 3 * time.Second
	instanceBurst   = 30
	instanceEvery   = time.Second
	defaultMaxDials = 256
)

// bot is a bot the place runs.
type bot struct {
	namespace, name, uid string
	// attachments maps each declared attachment to why the place cannot serve it, empty where it can.
	attachments map[string]string
	tools       []string
}

func (b *bot) key() string { return b.namespace + "/" + b.name }

// instance is a workload instance: a running bot's, one that ended, or a workload that runs no bot.
type instance struct {
	id, container string
	// bot is "<namespace>/<name>", or empty for a workload that runs no bot.
	bot string
	// botUID is the identity of the bot the instance was started for: a bot made again under its name
	// is another bot, and the instance is not its (BOT-1).
	botUID  string
	running bool
}

// key names the instance with its container, so a restart is a new instance (PIN-1).
func (i *instance) key() string { return i.id + "/" + i.container }

// entry is one path of a workspace: a file, or a link to another path.
type entry struct {
	content []byte
	link    string
}

// space is one workspace, by its bot's UID and its attachment.
type space struct {
	phase    workspace.Phase
	reason   string
	deleting bool
	files    map[string]entry
}

// dial is one open dial, or one reserved while its connection is taken over (DIAL-3).
type dial struct {
	// conn is nil while the dial is reserved.
	conn                    net.Conn
	instance, botUID, space string
	// last is when the dial last passed a byte, by the place's clock.
	last time.Time
}

// place is everything the reference holds, behind one lock.
type place struct {
	mu         sync.Mutex
	bots       map[string]*bot
	instances  map[string]*instance
	spaces     map[string]*space
	pins       map[string]string
	proofs     map[string]time.Time
	botLimits  map[string]*rate.Limiter
	instLimits map[string]*rate.Limiter
	dials      map[*dial]bool
	maxDials   int
	issuer     bool
	// offset moves the place's clock, which times idle dials (DIAL-3) and proofs (PROOF-1).
	offset time.Duration
}

func newPlace() *place {
	return &place{
		bots: map[string]*bot{}, instances: map[string]*instance{}, spaces: map[string]*space{},
		pins: map[string]string{}, proofs: map[string]time.Time{}, botLimits: map[string]*rate.Limiter{},
		instLimits: map[string]*rate.Limiter{}, dials: map[*dial]bool{}, maxDials: defaultMaxDials, issuer: true,
	}
}

func spaceKey(uid, attachment string) string { return uid + "/" + attachment }

func newID() string { return rand.Text()[:12] }

// clock is the place's clock for idle dials and proofs. The caller holds mu.
func (p *place) clock() time.Time { return time.Now().Add(p.offset) }

// closeDials closes every dial match takes. The caller holds mu.
func (p *place) closeDials(match func(*dial) bool) {
	for d := range p.dials {
		if match(d) {
			if d.conn != nil {
				_ = d.conn.Close()
			}
			delete(p.dials, d)
		}
	}
}

// removeBot forgets the bot and its limit, deletes its workspaces and closes its dials (DIAL-4). The
// caller holds mu.
func (p *place) removeBot(b *bot) {
	delete(p.bots, b.key())
	delete(p.botLimits, b.uid)
	for attachment := range b.attachments {
		delete(p.spaces, spaceKey(b.uid, attachment))
	}
	p.closeDials(func(d *dial) bool { return d.botUID == b.uid })
}

// dropInstance ends the instance's pin and dials (PIN-1, DIAL-4). The caller holds mu.
func (p *place) dropInstance(i *instance) {
	key := i.key()
	delete(p.pins, key)
	p.closeDials(func(d *dial) bool { return d.instance == key })
}

// setPhase moves a workspace behind the server, closing its dials when it stops running (DIAL-4).
// The caller holds mu.
func (p *place) setPhase(key string, s *space, phase workspace.Phase, deleting bool) {
	s.phase, s.deleting = phase, deleting
	s.reason = ""
	if phase == workspace.PhaseFailed {
		s.reason = "the reference's fixture failed it"
	}
	if phase != workspace.PhaseRunning || deleting {
		p.closeDials(func(d *dial) bool { return d.space == key })
	}
	// A stop and a recovery keep the data volume alone, as interface 3's workspaces do, so the reference
	// passes nothing a provider's workspace would fail.
	if phase == workspace.PhaseStopped || phase == workspace.PhaseFailed {
		maps.DeleteFunc(s.files, func(path string, _ entry) bool {
			return !strings.HasPrefix(path, workspace.DataPath+"/")
		})
	}
}

// openDials counts the open dials of an instance. The caller holds mu.
func (p *place) openDials(instance string) int {
	n := 0
	for d := range p.dials {
		if d.instance == instance {
			n++
		}
	}
	return n
}

// limiter answers the limiter of key in limits, made with every and burst where none is held. The
// caller holds mu.
func limiter(limits map[string]*rate.Limiter, key string, every time.Duration, burst int) *rate.Limiter {
	l, ok := limits[key]
	if !ok {
		l = rate.NewLimiter(rate.Every(every), burst)
		limits[key] = l
	}
	return l
}

// sortedTools answers the bot's tools in name order.
func (b *bot) sortedTools() []string { return slices.Sorted(slices.Values(b.tools)) }
