package resolver

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/braedonsaunders/ether/internal/core"
	"github.com/braedonsaunders/ether/internal/releaseparse"
)

type searchPreparationKey struct{}
type speculativeHealthKey struct{}

// Start at most two independent validations during indexer discovery. Selection
// still uses the complete, normally ranked result: a fast indexer cannot bypass
// an owner's pin, edition/language rules, or a better result from a slower one.
// All workers are cancelled and joined by the Resolve that owns them.
type searchPreparation struct {
	mu         sync.Mutex
	ctx        context.Context
	svc        *Service
	req        core.ResolveRequest
	search     core.SearchRequest
	profile    core.QualityProfile
	disabled   bool
	checks     candidateChecks
	byID       map[string]*candidateCheck
	identities map[string]bool
	releases   map[string]core.Release
}

func newSearchPreparation(ctx context.Context, s *Service, req core.ResolveRequest, search core.SearchRequest, profile core.QualityProfile, pin string) *searchPreparation {
	return &searchPreparation{ctx: ctx, svc: s, req: req, search: search, profile: profile,
		disabled: pin != "" || req.Prefer != "" || s.resolverCfg().OptimisticStart,
		byID:     map[string]*candidateCheck{}, identities: map[string]bool{}, releases: map[string]core.Release{}}
}

func (p *searchPreparation) arrived(batch []core.Release) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disabled || p.ctx.Err() != nil || len(p.byID) >= 2 {
		return
	}
	rs := append([]core.Release(nil), batch...)
	for i := range rs {
		if rs[i].Info.Height == 0 && rs[i].Info.Title == "" {
			rs[i].Info = releaseparse.Parse(rs[i].Name)
		}
	}
	cs, _, err := p.svc.rankCandidates(p.ctx, p.req, p.search, p.profile, rs, nil, "")
	if err != nil {
		return
	}
	for _, c := range cs {
		if len(p.byID) >= 2 {
			break
		}
		if c.rel.Protocol == core.ProtocolTorrent || p.byID[c.rel.ID] != nil {
			continue
		}
		// Same named/size reposts are correlated; do not spend both speculative
		// slots on them. Full validation may still use either after discovery.
		identity := strings.ToLower(strings.TrimSpace(c.rel.Name)) + "|" + strconv.FormatInt(c.rel.Size, 10)
		if p.identities[identity] {
			continue
		}
		p.identities[identity] = true
		p.releases[c.rel.ID] = c.rel
		p.byID[c.rel.ID] = p.checks.start(context.WithValue(p.ctx, speculativeHealthKey{}, true), p.svc, c.rel)
	}
}

func (p *searchPreparation) attach(cs []candidate) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range cs {
		r := cs[i].rel
		if original, ok := p.releases[r.ID]; ok && original.Name == r.Name && original.Size == r.Size && original.NZBURL == r.NZBURL && original.Protocol == r.Protocol {
			cs[i].prepared = p.byID[r.ID]
		}
	}
}
func (p *searchPreparation) close() { p.checks.close() }
