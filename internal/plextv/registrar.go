package plextv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/ether-media/ether/internal/config"
	"github.com/ether-media/ether/internal/remote"
)

// ConfigStore is the part of *config.Store the registrar uses.
type ConfigStore interface {
	Get() config.Config
	Update(fn func(*config.Config) error) error
}

// UserSync receives the owner and the friend list (implemented by
// internal/auth).
type UserSync interface {
	SetOwner(ctx context.Context, owner Account, serverToken string) error
	SyncFriends(ctx context.Context, friends []Friend) error
}

// WANDetector finds the public IP (remote.WANDetector).
type WANDetector interface {
	Detect(ctx context.Context) (net.IP, error)
}

// PortMapper keeps the remote-access port forwarded (remote.PortMapper).
type PortMapper interface {
	Ensure(ctx context.Context, internal, external int) (remote.MappingStatus, error)
	Status() remote.MappingStatus
	Close(ctx context.Context)
}

// Status is the plex.tv / remote-access state for the admin dashboard.
type Status struct {
	Claimed           bool   `json:"claimed"`
	OwnerUsername     string `json:"ownerUsername,omitempty"`
	MachineIdentifier string `json:"machineIdentifier"`

	LANAddresses []string            `json:"lanAddresses"`
	PublicIP     string              `json:"publicIp,omitempty"`
	PublicIPErr  string              `json:"publicIpError,omitempty"`
	Connections  []remote.Connection `json:"connections"`

	PublishedURIs    []string  `json:"publishedUris"`
	LastPublish      time.Time `json:"lastPublish,omitempty"`
	LastPublishError string    `json:"lastPublishError,omitempty"`
	RegisterError    string    `json:"registerError,omitempty"` // servers.xml (non-fatal)

	// Listed reports whether plex.tv's resources list contains this server;
	// ResourceConnections is what clients will be offered.
	Listed              bool                 `json:"listed"`
	ResourceConnections []ResourceConnection `json:"resourceConnections,omitempty"`
	PublicAddress       string               `json:"publicAddress,omitempty"` // as plex.tv sees us
	PlexPass            bool                 `json:"plexPass"`                // owner subscription active

	RemoteAccess bool                 `json:"remoteAccess"`
	Reachability *remote.Reachability `json:"reachability,omitempty"`
	UPnP         remote.MappingStatus `json:"upnp"`

	Cert      CertInfo `json:"cert"`
	CertError string   `json:"certError,omitempty"`

	Friends         int       `json:"friends"`
	LastFriendSync  time.Time `json:"lastFriendSync,omitempty"`
	FriendSyncError string    `json:"friendSyncError,omitempty"`
}

// RegistrarOptions configures a Registrar. Only Config is required; nil
// collaborators get production defaults or are disabled.
type RegistrarOptions struct {
	Config ConfigStore
	// Client carries the server identity; built from Config when nil.
	Client  *Client
	BaseURL string // for the default client (tests)
	Version string // PMS version reported to plex.tv
	Users   UserSync
	WAN     WANDetector
	LAN     func() ([]net.IP, error)
	Mapper  PortMapper // nil creates a remote.PortMapper
	Reach   *remote.ReachabilityChecker
	Certs   *CertManager // nil disables certificate management

	// Intervals (defaults in parentheses).
	Tick                 time.Duration // loop granularity (15s)
	AddressInterval      time.Duration // LAN/WAN re-check (5m)
	PublishInterval      time.Duration // unconditional re-publish (1h)
	FriendSyncInterval   time.Duration // access token sync (30m)
	MappingInterval      time.Duration // UPnP/NAT-PMP renew check (5m)
	CertInterval         time.Duration // certificate renewal check (12h)
	ReachabilityInterval time.Duration // remote self-test (30m)

	// PublishPlexDirect also publishes the https plex.direct URIs. Off by
	// default: PMS publishes plain http URIs and plex.tv synthesises the
	// plex.direct https variants for clients asking with includeHttps=1.
	PublishPlexDirect bool

	Logger *slog.Logger
	Now    func() time.Time
}

// Registrar runs Ether's plex.tv presence: claim, connection publishing,
// friend sync, certificate renewal, port mapping and reachability.
type Registrar struct {
	o      RegistrarOptions
	client *Client
	log    *slog.Logger

	wake chan struct{}

	runMu  sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	st        Status
	lastLAN   []string
	lastWAN   string
	next      map[string]time.Time
	pubFail   int
	forcePub  bool
	mappingOn bool
}

const (
	taskAddress = "address"
	taskPublish = "publish"
	taskFriends = "friends"
	taskMapping = "mapping"
	taskCert    = "cert"
	taskReach   = "reach"
)

// NewRegistrar validates options and returns a registrar; call Start.
func NewRegistrar(o RegistrarOptions) (*Registrar, error) {
	if o.Config == nil {
		return nil, errors.New("plextv: registrar needs Config")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&o.Tick, 15*time.Second)
	def(&o.AddressInterval, 5*time.Minute)
	def(&o.PublishInterval, time.Hour)
	def(&o.FriendSyncInterval, 30*time.Minute)
	def(&o.MappingInterval, 5*time.Minute)
	def(&o.CertInterval, 12*time.Hour)
	def(&o.ReachabilityInterval, 30*time.Minute)
	if o.Version == "" {
		o.Version = DefaultServerVersion
	}
	client := o.Client
	if client == nil {
		var err error
		client, err = New(Options{BaseURL: o.BaseURL, Identity: ServerIdentity(o.Config.Get(), o.Version), Logger: o.Logger})
		if err != nil {
			return nil, err
		}
	}
	if o.WAN == nil {
		o.WAN = remote.NewWANDetector(nil, o.Logger)
	}
	if o.LAN == nil {
		o.LAN = func() ([]net.IP, error) { return remote.LANIPs(remote.LANOptions{}) }
	}
	if o.Mapper == nil {
		o.Mapper = remote.NewPortMapper(remote.PortMapperOptions{Description: "Ether", Logger: o.Logger})
	}
	if o.Reach == nil {
		o.Reach = remote.NewReachabilityChecker(nil)
	}
	r := &Registrar{
		o:      o,
		client: client,
		log:    o.Logger.With("component", "registrar"),
		wake:   make(chan struct{}, 1),
		next:   map[string]time.Time{},
	}
	r.refreshConfigStatus(o.Config.Get())
	return r, nil
}

// Client returns the server-identity plex.tv client.
func (r *Registrar) Client() *Client { return r.client }

// SignInClient returns a client with the setup wizard's identity for PIN
// sign-in (CreatePIN, AuthURL, WaitPIN).
func (r *Registrar) SignInClient() *Client {
	return r.client.WithIdentity(AppIdentity(r.o.Config.Get(), ""))
}

// Start runs the background loop until ctx ends or Stop is called.
func (r *Registrar) Start(ctx context.Context) error {
	r.runMu.Lock()
	defer r.runMu.Unlock()
	if r.cancel != nil {
		return errors.New("plextv: registrar already started")
	}
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan struct{})
	go r.loop(ctx, r.done)
	return nil
}

// Stop ends the loop and removes any port mapping.
func (r *Registrar) Stop() {
	r.runMu.Lock()
	cancel, done := r.cancel, r.done
	r.cancel, r.done = nil, nil
	r.runMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	r.o.Mapper.Close(ctx)
	r.mu.Lock()
	r.st.UPnP = r.o.Mapper.Status()
	r.mappingOn = false
	r.mu.Unlock()
}

// Trigger asks the loop to re-check addresses and re-publish now (e.g. after
// a config change).
func (r *Registrar) Trigger() {
	r.mu.Lock()
	r.forcePub = true
	r.next[taskAddress] = time.Time{}
	r.next[taskMapping] = time.Time{}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Status returns a snapshot for the admin dashboard.
func (r *Registrar) Status() Status {
	r.mu.Lock()
	st := r.st
	r.mu.Unlock()
	st.LANAddresses = slices.Clone(st.LANAddresses)
	st.Connections = slices.Clone(st.Connections)
	st.PublishedURIs = slices.Clone(st.PublishedURIs)
	st.ResourceConnections = slices.Clone(st.ResourceConnections)
	st.UPnP = r.o.Mapper.Status()
	if r.o.Certs != nil {
		st.Cert = r.o.Certs.Info()
	}
	return st
}

func (r *Registrar) refreshConfigStatus(cfg config.Config) {
	r.mu.Lock()
	r.st.Claimed = cfg.Plex.Claimed && cfg.Plex.Token != ""
	r.st.OwnerUsername = cfg.Plex.OwnerUsername
	r.st.MachineIdentifier = cfg.Server.MachineIdentifier
	r.st.RemoteAccess = cfg.Plex.RemoteAccess
	r.mu.Unlock()
}

func (r *Registrar) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(r.o.Tick)
	defer t.Stop()
	for {
		r.runDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.wake:
		}
	}
}

// due reports whether task should run now, and schedules its next run.
func (r *Registrar) due(task string, every time.Duration) bool {
	now := r.o.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Before(r.next[task]) {
		return false
	}
	r.next[task] = now.Add(every)
	return true
}

func (r *Registrar) schedule(task string, at time.Time) {
	r.mu.Lock()
	r.next[task] = at
	r.mu.Unlock()
}

func (r *Registrar) runDue(ctx context.Context) {
	cfg := r.o.Config.Get()
	r.refreshConfigStatus(cfg)
	if r.due(taskMapping, r.o.MappingInterval) {
		r.ensureMapping(ctx, cfg)
	}
	if r.due(taskAddress, r.o.AddressInterval) {
		r.refreshAddresses(ctx, cfg)
	}
	if ctx.Err() != nil {
		return
	}
	if r.o.Certs != nil && r.due(taskCert, r.o.CertInterval) {
		_ = r.RenewCertificate(ctx, false)
	}
	claimed := cfg.Plex.Claimed && cfg.Plex.Token != ""
	if !claimed {
		return
	}
	r.mu.Lock()
	force := r.forcePub
	r.mu.Unlock()
	if force || r.due(taskPublish, r.o.PublishInterval) {
		if err := r.PublishNow(ctx); err != nil && ctx.Err() == nil {
			r.mu.Lock()
			r.pubFail++
			n := r.pubFail
			r.mu.Unlock()
			backoff := time.Minute << min(n-1, 5) // 1m .. 32m
			r.mu.Lock()
			r.forcePub = false // the back-off schedule retries with fresh state
			r.mu.Unlock()
			r.schedule(taskPublish, r.o.Now().Add(backoff))
		}
	}
	if r.due(taskFriends, r.o.FriendSyncInterval) {
		_ = r.SyncFriendsNow(ctx)
	}
	if cfg.Plex.RemoteAccess && r.due(taskReach, r.o.ReachabilityInterval) {
		r.CheckReachability(ctx)
	}
}

func (r *Registrar) ensureMapping(ctx context.Context, cfg config.Config) {
	want := cfg.Plex.RemoteAccess && cfg.Plex.UseUPnP
	r.mu.Lock()
	on := r.mappingOn
	r.mu.Unlock()
	if !want {
		if on {
			r.o.Mapper.Close(ctx)
			r.mu.Lock()
			r.mappingOn = false
			r.forcePub = true
			r.mu.Unlock()
		}
		return
	}
	prev := r.o.Mapper.Status()
	st, err := r.o.Mapper.Ensure(ctx, serverPort(cfg), publicPort(cfg))
	r.mu.Lock()
	r.mappingOn = true
	if st.Active && (st.ExternalPort != prev.ExternalPort || !prev.Active) {
		r.forcePub = true
	}
	r.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		r.log.Debug("port mapping failed", "err", err)
	}
}

func serverPort(cfg config.Config) int {
	if cfg.Server.Port > 0 {
		return cfg.Server.Port
	}
	return 32400
}

// publicPort is the WAN port: the manual forward, else the server port.
func publicPort(cfg config.Config) int {
	if cfg.Plex.PublicPort > 0 {
		return cfg.Plex.PublicPort
	}
	return serverPort(cfg)
}

func (r *Registrar) refreshAddresses(ctx context.Context, cfg config.Config) {
	var lan []string
	if ips, err := r.o.LAN(); err == nil {
		for _, ip := range ips {
			lan = append(lan, ip.String())
		}
	} else {
		r.log.Warn("lan address enumeration failed", "err", err)
	}
	wan, wanErr := "", ""
	if cfg.Plex.RemoteAccess {
		switch {
		case cfg.Plex.PublicHost != "":
			wan = cfg.Plex.PublicHost
		default:
			if m := r.o.Mapper.Status(); m.Active && m.ExternalIP != "" && remote.IsPublicIP(net.ParseIP(m.ExternalIP)) {
				wan = m.ExternalIP
			} else if ip, err := r.o.WAN.Detect(ctx); err == nil {
				wan = ip.String()
			} else {
				wanErr = err.Error()
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Equal(lan, r.lastLAN) || wan != r.lastWAN {
		if r.lastLAN != nil || r.lastWAN != "" {
			r.log.Info("network addresses changed", "lan", lan, "wan", wan)
		}
		r.forcePub = true
	}
	r.lastLAN, r.lastWAN = lan, wan
	r.st.LANAddresses = lan
	r.st.PublicIP = wan
	r.st.PublicIPErr = wanErr
}

// connections builds the connection list from the latest addresses.
func (r *Registrar) connections(cfg config.Config) []remote.Connection {
	r.mu.Lock()
	lanS, wanS := slices.Clone(r.lastLAN), r.lastWAN
	r.mu.Unlock()
	var lan []net.IP
	for _, s := range lanS {
		if ip := net.ParseIP(s); ip != nil {
			lan = append(lan, ip)
		}
	}
	var wan net.IP
	extraURLs := slices.Clone(cfg.Plex.CustomURLs)
	if wanS != "" {
		if ip := net.ParseIP(wanS); ip != nil {
			wan = ip
		} else {
			// PublicHost is a DNS name: publish it as a custom URL.
			extraURLs = append(extraURLs, fmt.Sprintf("http://%s", net.JoinHostPort(wanS, fmt.Sprint(publicPort(cfg)))))
		}
	}
	hash := cfg.Plex.CertHash
	if r.o.Certs != nil {
		if info := r.o.Certs.Info(); info.Present && !info.SelfSigned && info.Hash != "" {
			hash = info.Hash
		} else {
			hash = ""
		}
	}
	return remote.BuildConnections(remote.BuildOptions{
		LAN: lan, WAN: wan, LocalPort: serverPort(cfg), PublicPort: publicPort(cfg),
		CertHash: hash, Secure: cfg.Plex.SecureConnections, CustomURLs: extraURLs,
	})
}

func publishable(cs []remote.Connection, includePlexDirect bool) []string {
	var out []string
	for _, c := range cs {
		if c.Protocol == "https" && !c.Custom && !includePlexDirect {
			continue
		}
		out = append(out, c.URI)
	}
	return out
}

// PublishNow publishes the current connections to plex.tv and verifies the
// server appears in the resources list.
func (r *Registrar) PublishNow(ctx context.Context) error {
	cfg := r.o.Config.Get()
	if cfg.Plex.Token == "" {
		return errors.New("plextv: not claimed")
	}
	r.mu.Lock()
	haveAddrs := r.lastLAN != nil || r.lastWAN != ""
	r.mu.Unlock()
	if !haveAddrs {
		r.refreshAddresses(ctx, cfg)
	}
	conns := r.connections(cfg)
	uris := publishable(conns, r.o.PublishPlexDirect)
	if !cfg.Plex.RemoteAccess {
		// Only LAN and custom URLs when remote access is off.
		var lanOnly []string
		for _, c := range conns {
			if (c.Local || c.Custom) && slices.Contains(uris, c.URI) {
				lanOnly = append(lanOnly, c.URI)
			}
		}
		uris = lanOnly
	}
	now := r.o.Now()
	err := r.client.PublishConnections(ctx, cfg.Plex.Token, uris)

	r.mu.Lock()
	r.st.Connections = conns
	r.st.LastPublish = now
	if err != nil {
		r.st.LastPublishError = err.Error()
		r.mu.Unlock()
		r.log.Warn("publish connections failed", "err", err)
		return err
	}
	r.st.PublishedURIs = uris
	r.st.LastPublishError = ""
	r.pubFail = 0
	r.forcePub = false
	r.next[taskPublish] = now.Add(r.o.PublishInterval)
	lan, wan := slices.Clone(r.lastLAN), r.lastWAN
	r.mu.Unlock()
	r.log.Info("published connections", "count", len(uris))

	mapping := "unknown"
	if cfg.Plex.RemoteAccess {
		if m := r.o.Mapper.Status(); m.Active || cfg.Plex.PublicPort > 0 {
			mapping = "mapped"
		}
	} else {
		mapping = "unmapped"
	}
	reg := ServerRegistration{
		Name: cfg.Server.FriendlyName, Version: r.o.Version, LocalAddresses: lan, MappingState: mapping,
	}
	if cfg.Plex.RemoteAccess {
		reg.Port = publicPort(cfg)
		if net.ParseIP(wan) != nil {
			reg.Address = wan
		}
	}
	regErr := r.client.RegisterServer(ctx, cfg.Plex.Token, reg)
	if regErr != nil && ctx.Err() == nil {
		r.log.Debug("servers.xml registration failed (non-fatal)", "err", regErr)
	}
	res, ferr := r.client.FindServer(ctx, cfg.Plex.Token, cfg.Server.MachineIdentifier)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.st.RegisterError = ""
	if regErr != nil {
		r.st.RegisterError = regErr.Error()
	}
	if ferr == nil {
		r.st.Listed = true
		r.st.ResourceConnections = res.Connections
		r.st.PublicAddress = res.PublicAddress
	} else if ctx.Err() == nil {
		r.st.Listed = false
		r.st.ResourceConnections = nil
	}
	return nil
}

// SyncFriendsNow refreshes friends and their access tokens.
func (r *Registrar) SyncFriendsNow(ctx context.Context) error {
	cfg := r.o.Config.Get()
	if cfg.Plex.Token == "" {
		return errors.New("plextv: not claimed")
	}
	friends, err := r.client.Friends(ctx, cfg.Plex.Token)
	if err == nil && r.o.Users != nil {
		err = r.o.Users.SyncFriends(ctx, friends)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.st.LastFriendSync = r.o.Now()
	if err != nil {
		r.st.FriendSyncError = err.Error()
		r.log.Warn("friend sync failed", "err", err)
		return err
	}
	n := 0
	for _, f := range friends {
		if !f.Owned {
			n++
		}
	}
	r.st.Friends = n
	r.st.FriendSyncError = ""
	return nil
}

// CheckReachability probes the WAN connections and records the result.
func (r *Registrar) CheckReachability(ctx context.Context) remote.Reachability {
	cfg := r.o.Config.Get()
	var uris []string
	for _, c := range r.connections(cfg) {
		if !c.Local {
			uris = append(uris, c.URI)
		}
	}
	res := r.o.Reach.Check(ctx, uris, cfg.Server.MachineIdentifier)
	r.mu.Lock()
	r.st.Reachability = &res
	r.mu.Unlock()
	return res
}

// RenewCertificate runs the certificate manager; force bypasses the
// failure back-off (admin "retry now").
func (r *Registrar) RenewCertificate(ctx context.Context, force bool) error {
	if r.o.Certs == nil {
		return errors.New("plextv: certificate management disabled")
	}
	cfg := r.o.Config.Get()
	r.mu.Lock()
	hosts := slices.Clone(r.lastLAN)
	r.mu.Unlock()
	before := r.o.Certs.Info()
	info, err := r.o.Certs.Ensure(ctx, cfg.Plex.Token, hosts, force)
	r.mu.Lock()
	r.st.CertError = ""
	if err != nil {
		r.st.CertError = err.Error()
	}
	r.mu.Unlock()
	if info.Present && (info.Fingerprint != before.Fingerprint || cfg.Plex.CertFile != info.CertFile || cfg.Plex.CertHash != info.Hash) {
		uerr := r.o.Config.Update(func(c *config.Config) error {
			c.Plex.CertFile, c.Plex.KeyFile = info.CertFile, info.KeyFile
			if !info.SelfSigned {
				c.Plex.CertHash = info.Hash
			} else {
				c.Plex.CertHash = ""
			}
			return nil
		})
		if uerr != nil {
			r.log.Warn("persist certificate paths", "err", uerr)
		}
		r.mu.Lock()
		r.forcePub = true
		r.mu.Unlock()
	}
	return err
}

// Claim claims the server for the account behind userToken (from PIN
// sign-in), persists the server token and owner, informs UserSync and
// triggers publishing.
func (r *Registrar) Claim(ctx context.Context, userToken string) (*ClaimResult, error) {
	res, err := r.client.Claim(ctx, userToken)
	if err != nil {
		return nil, err
	}
	if err := r.o.Config.Update(func(c *config.Config) error {
		c.Plex.Token = res.ServerToken
		c.Plex.OwnerPlexID = res.Owner.ID
		c.Plex.OwnerUsername = res.Owner.Username
		c.Plex.Claimed = true
		return nil
	}); err != nil {
		return nil, fmt.Errorf("plextv: persist claim: %w", err)
	}
	if r.o.Users != nil {
		if err := r.o.Users.SetOwner(ctx, res.Owner, res.ServerToken); err != nil {
			return nil, fmt.Errorf("plextv: set owner: %w", err)
		}
	}
	r.mu.Lock()
	r.st.PlexPass = res.Owner.Subscription.Active
	r.next[taskFriends] = time.Time{}
	r.next[taskCert] = time.Time{}
	r.mu.Unlock()
	r.refreshConfigStatus(r.o.Config.Get())
	r.log.Info("server claimed", "owner", res.Owner.Username, "method", res.Method)
	r.Trigger()
	return res, nil
}

// Unclaim forgets the server token (sign out). The plex.tv device record
// stays until the owner removes it from their account.
func (r *Registrar) Unclaim(ctx context.Context) error {
	if err := r.o.Config.Update(func(c *config.Config) error {
		c.Plex.Token = ""
		c.Plex.Claimed = false
		return nil
	}); err != nil {
		return err
	}
	r.mu.Lock()
	r.st.Listed = false
	r.st.PublishedURIs = nil
	r.st.ResourceConnections = nil
	r.mu.Unlock()
	r.refreshConfigStatus(r.o.Config.Get())
	return nil
}

// RefreshOwner re-reads the owner's account (Plex Pass state) and passes it
// to UserSync; call at startup.
func (r *Registrar) RefreshOwner(ctx context.Context) error {
	cfg := r.o.Config.Get()
	if cfg.Plex.Token == "" {
		return errors.New("plextv: not claimed")
	}
	acct, err := r.client.User(ctx, cfg.Plex.Token)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.st.PlexPass = acct.Subscription.Active
	r.mu.Unlock()
	if r.o.Users != nil {
		return r.o.Users.SetOwner(ctx, *acct, cfg.Plex.Token)
	}
	return nil
}
