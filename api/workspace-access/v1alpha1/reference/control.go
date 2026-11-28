package reference

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// control serves the fixture's subcommands that change or read the place, on a loopback listener of
// its own that no client of the protocol is given.
func (p *place) control() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fixture", func(w http.ResponseWriter, r *http.Request) {
		var args []string
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil || len(args) == 0 {
			http.Error(w, "the body is the subcommand and its arguments, as a JSON array", http.StatusBadRequest)
			return
		}
		answer, err := p.run(args[0], args[1:])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, answer)
	})
	return mux
}

// controlled are the subcommands control serves.
var controlled = map[string]bool{
	"bot": true, "remake": true, "remove": true, "instance": true, "restart": true, "end": true,
	"workspace": true, "seed": true, "file": true, "link": true, "exists": true, "issuer": true, "max-dials": true, "clock": true,
	"limits": true,
}

// BotAnswer is what bot and remake print.
type BotAnswer struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// InstanceAnswer is what instance prints.
type InstanceAnswer struct {
	Instance string `json:"instance"`
}

// FileAnswer is what file prints: whether the path holds anything, its content, and the path it links
// to where it is a link.
type FileAnswer struct {
	Exists  bool   `json:"exists"`
	Content string `json:"content,omitempty"`
	Link    string `json:"link,omitempty"`
}

// ExistsAnswer is what exists prints.
type ExistsAnswer struct {
	Exists bool `json:"exists"`
}

// LimitsAnswer is what limits prints: the bursts of the bot's and the instance's limits, and the
// seconds each takes to give back one call (LIMIT-1, LIMIT-2).
type LimitsAnswer struct {
	BotBurst             int `json:"botBurst"`
	BotEverySeconds      int `json:"botEverySeconds"`
	InstanceBurst        int `json:"instanceBurst"`
	InstanceEverySeconds int `json:"instanceEverySeconds"`
}

// MaxDialsAnswer is what max-dials prints: the cap it replaced, for a caller to put back.
type MaxDialsAnswer struct {
	Previous int `json:"previous"`
}

// done is what a subcommand prints that answers nothing.
type done struct{}

// stringList is a flag taken more than once.
type stringList []string

func (s *stringList) String() string     { return fmt.Sprint(*s) }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func (p *place) run(name string, args []string) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch name {
	case "bot":
		return p.addBot(args)
	case "remake":
		if len(args) != 2 {
			return nil, errors.New("remake takes NAMESPACE NAME")
		}
		b, ok := p.bots[args[0]+"/"+args[1]]
		if !ok {
			return nil, errors.New("no such bot")
		}
		// The earlier bot's instances run on, as a workload outlives its owner for a while, and reach
		// nothing (BOT-1).
		p.removeBot(b)
		made := &bot{namespace: b.namespace, name: b.name, uid: newID(), attachments: b.attachments, tools: b.tools}
		p.bots[made.key()] = made
		return BotAnswer{Namespace: made.namespace, Name: made.name, UID: made.uid}, nil
	case "remove":
		if len(args) != 2 {
			return nil, errors.New("remove takes NAMESPACE NAME")
		}
		if b, ok := p.bots[args[0]+"/"+args[1]]; ok {
			p.removeBot(b)
		}
		return done{}, nil
	case "instance":
		return p.addInstance(args)
	case "restart", "end":
		if len(args) != 1 {
			return nil, fmt.Errorf("%s takes INSTANCE", name)
		}
		inst, ok := p.instances[args[0]]
		if !ok {
			return nil, errors.New("no such instance")
		}
		p.dropInstance(inst)
		if name == "end" {
			inst.running = false
			delete(p.instLimits, inst.id)
		} else {
			inst.container = newID()
		}
		return InstanceAnswer{Instance: inst.id}, nil
	case "workspace":
		return p.moveSpace(args)
	case "seed":
		return p.seedSpace(args)
	case "file", "link", "exists":
		return p.reachSpace(name, args)
	case "issuer":
		if len(args) != 1 || (args[0] != "--on" && args[0] != "--off") {
			return nil, errors.New("issuer takes --on or --off")
		}
		p.issuer = args[0] == "--on"
		return done{}, nil
	case "max-dials":
		n, err := strconv.Atoi(single(args))
		if err != nil || n < 0 {
			return nil, errors.New("max-dials takes N")
		}
		previous := p.maxDials
		p.maxDials = n
		return MaxDialsAnswer{Previous: previous}, nil
	case "clock":
		flags := flag.NewFlagSet("clock", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		advance := flags.Duration("advance", 0, "")
		if err := flags.Parse(args); err != nil {
			return nil, errors.New("clock takes --advance DURATION")
		}
		p.offset += *advance
		return done{}, nil
	case "limits":
		if len(args) != 0 {
			return nil, errors.New("limits takes no argument")
		}
		return LimitsAnswer{
			BotBurst: botBurst, BotEverySeconds: int(botEvery / time.Second),
			InstanceBurst: instanceBurst, InstanceEverySeconds: int(instanceEvery / time.Second),
		}, nil
	}
	return nil, fmt.Errorf("unknown subcommand %q", name)
}

func single(args []string) string {
	if len(args) != 1 {
		return ""
	}
	return args[0]
}

// addBot places a bot with its attachments, some of which the place cannot serve, and its tools. The
// caller holds mu.
func (p *place) addBot(args []string) (any, error) {
	flags := flag.NewFlagSet("bot", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var attachments, unsupported, tools stringList
	flags.Var(&attachments, "attachment", "")
	flags.Var(&unsupported, "unsupported", "")
	flags.Var(&tools, "tool", "")
	positional, err := parseInterleaved(flags, args)
	if err != nil || len(positional) != 2 {
		return nil, errors.New("bot takes NAMESPACE NAME, --attachment, --unsupported and --tool")
	}
	b := &bot{namespace: positional[0], name: positional[1], uid: newID(), attachments: map[string]string{}, tools: tools}
	for _, a := range attachments {
		b.attachments[a] = ""
	}
	for _, a := range unsupported {
		b.attachments[a] = "the reference's fixture declared it unsupported"
	}
	if old, ok := p.bots[b.key()]; ok {
		p.removeBot(old)
	}
	p.bots[b.key()] = b
	return BotAnswer{Namespace: b.namespace, Name: b.name, UID: b.uid}, nil
}

// addInstance starts a workload instance of a bot, or one that runs no bot with --no-bot. The caller
// holds mu.
func (p *place) addInstance(args []string) (any, error) {
	inst := &instance{id: newID(), container: newID(), running: true}
	switch {
	case len(args) == 1 && args[0] == "--no-bot":
	case len(args) == 2:
		inst.bot = args[0] + "/" + args[1]
		if b, ok := p.bots[inst.bot]; ok {
			inst.botUID = b.uid
		}
	default:
		return nil, errors.New("instance takes NAMESPACE NAME, or --no-bot")
	}
	p.instances[inst.id] = inst
	return InstanceAnswer{Instance: inst.id}, nil
}

// moveSpace moves a workspace, by its bot's UID and its attachment, to a phase behind the server.
// The caller holds mu.
func (p *place) moveSpace(args []string) (any, error) {
	if len(args) != 3 {
		return nil, errors.New("workspace takes UID ATTACHMENT --stopped|--failed|--pending|--deleting|--running")
	}
	key := spaceKey(args[0], args[1])
	sp, ok := p.spaces[key]
	if !ok {
		return nil, errors.New("no such workspace")
	}
	switch args[2] {
	case "--stopped":
		p.setPhase(key, sp, workspace.PhaseStopped, false)
	case "--failed":
		p.setPhase(key, sp, workspace.PhaseFailed, false)
	case "--pending":
		p.setPhase(key, sp, workspace.PhasePending, false)
	case "--deleting":
		p.setPhase(key, sp, workspace.PhaseStopping, true)
	case "--running":
		p.setPhase(key, sp, workspace.PhaseRunning, false)
	default:
		return nil, errors.New("workspace takes --stopped, --failed, --pending, --deleting or --running")
	}
	return done{}, nil
}

// seedSpace writes a file on a workspace's data volume, as the bot's work there does. The caller
// holds mu.
func (p *place) seedSpace(args []string) (any, error) {
	if len(args) != 4 {
		return nil, errors.New("seed takes UID ATTACHMENT PATH CONTENT")
	}
	sp, ok := p.spaces[spaceKey(args[0], args[1])]
	if !ok {
		return nil, errors.New("no such workspace")
	}
	if path.Clean(args[2]) != args[2] || !strings.HasPrefix(args[2], workspace.DataPath+"/") {
		return nil, fmt.Errorf("seed writes a clean path under %s/ alone", workspace.DataPath)
	}
	sp.files[args[2]] = entry{content: []byte(args[3])}
	return done{}, nil
}

// reachSpace reads a path of a workspace, plants a link in it, or says whether it exists. A link may
// stand at any component of a path a write names, a directory's among them (FILE-1). The caller holds
// mu.
func (p *place) reachSpace(name string, args []string) (any, error) {
	want := map[string]int{"file": 3, "link": 4, "exists": 2}[name]
	if len(args) != want {
		return nil, fmt.Errorf("%s takes UID ATTACHMENT and its paths", name)
	}
	sp, ok := p.spaces[spaceKey(args[0], args[1])]
	if name == "exists" {
		return ExistsAnswer{Exists: ok}, nil
	}
	if !ok {
		return nil, errors.New("no such workspace")
	}
	if name == "link" {
		sp.files[args[2]] = entry{link: args[3]}
		if _, held := sp.files[args[3]]; !held {
			sp.files[args[3]] = entry{content: []byte("the link's target\n")}
		}
		return FileAnswer{Exists: true, Link: args[3]}, nil
	}
	e, held := sp.files[args[2]]
	if !held {
		return FileAnswer{}, nil
	}
	return FileAnswer{Exists: true, Content: base64.StdEncoding.EncodeToString(e.content), Link: e.link}, nil
}
