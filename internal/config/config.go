package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

type Config struct {
	System      SystemConfig      `yaml:"system"`
	Interfaces  []InterfaceConfig `yaml:"interfaces"`
	VLANs       []VLANConfig      `yaml:"vlans"`
	HealthCheck HealthCheckConfig `yaml:"healthCheck"`
	PPPoE       PPPoEConfig       `yaml:"pppoe"`
	USBTether   USBTetherConfig   `yaml:"usbTethering"`
	Firewall    FirewallConfig    `yaml:"firewall"`
	QoS         QoSConfig         `yaml:"qos"`
	DNS         DNSConfig         `yaml:"dns"`
	DHCP        DHCPConfig        `yaml:"dhcp"`
	IPv6        IPv6Config        `yaml:"ipv6"`
	VPN         VPNConfig         `yaml:"vpn"`
	OpenVPN     OpenVPNConfig     `yaml:"openvpn"`
	Routing     RoutingConfig     `yaml:"routing"`
	NAS         NASConfig         `yaml:"nas"`
	Syslog      SyslogConfig      `yaml:"syslog"`
	NTP         NTPConfig         `yaml:"ntp"`
	Storage     StorageConfig     `yaml:"storage"`
}

type SystemConfig struct {
	Hostname          string    `yaml:"hostname"`
	Timezone          string    `yaml:"timezone"`
	Language          string    `yaml:"language"`
	AdminPasswordHash string    `yaml:"adminPasswordHash"`
	SessionSecret     string    `yaml:"sessionSecret"`
	Domain            string    `yaml:"domain"`
	WebPort           int       `yaml:"webPort"`
	WebBind           string    `yaml:"webBind"`
	TLS               TLSConfig `yaml:"tls"`
}

type TLSConfig struct {
	Mode       string           `yaml:"mode"`
	CertFile   string           `yaml:"certFile"`
	KeyFile    string           `yaml:"keyFile"`
	SelfSigned SelfSignedConfig `yaml:"selfSigned"`
	ACME       ACMEConfig       `yaml:"acme"`
	Mkcert     MkcertConfig     `yaml:"mkcert"`
}

type SelfSignedConfig struct {
	CN        string   `yaml:"cn"`
	ValidDays int      `yaml:"validDays"`
	SANs      []string `yaml:"sans"`
}

type ACMEConfig struct {
	Enabled      bool                `yaml:"enabled"`
	Email        string              `yaml:"email"`
	Domain       string              `yaml:"domain"`
	Provider     string              `yaml:"provider"`
	DNSChallenge DNSChallengeConfig  `yaml:"dnsChallenge"`
}

type DNSChallengeConfig struct {
	Provider string `yaml:"provider"`
	APIToken string `yaml:"apiToken"`
}

type MkcertConfig struct {
	CAInstalled bool `yaml:"caInstalled"`
}

type InterfaceConfig struct {
	ID       string `yaml:"id"`
	Device   string `yaml:"device"`
	Label    string `yaml:"label"`
	Role     string `yaml:"role"`
	Type     string `yaml:"type"`
	Address  string `yaml:"address"`
	Address6 string `yaml:"address6"`
	MTU      int    `yaml:"mtu"`
	MAC      string `yaml:"mac"`
	CloneMAC string `yaml:"cloneMAC"`
	IPv6     string `yaml:"ipv6"`
}

type VLANConfig struct {
	ID       string     `yaml:"id"`
	Parent   string     `yaml:"parent"`
	VID      int        `yaml:"vid"`
	Label    string     `yaml:"label"`
	Role     string     `yaml:"role"`
	Type     string     `yaml:"type"`
	Address  string     `yaml:"address"`
	MTU      int        `yaml:"mtu"`
	Isolated bool       `yaml:"isolated"`
	DHCP     VLANDHCPConfig `yaml:"dhcp"`
}

type VLANDHCPConfig struct {
	Enabled    bool   `yaml:"enabled"`
	RangeStart string `yaml:"rangeStart"`
	RangeEnd   string `yaml:"rangeEnd"`
	LeaseTime  string `yaml:"leaseTime"`
}

type HealthCheckConfig struct {
	Enabled bool               `yaml:"enabled"`
	Checks  []HealthCheckEntry `yaml:"checks"`
}

type HealthCheckEntry struct {
	Name             string              `yaml:"name"`
	Interface        string              `yaml:"interface"`
	Targets          []HealthCheckTarget `yaml:"targets"`
	Interval         string              `yaml:"interval"`
	Timeout          string              `yaml:"timeout"`
	FailureThreshold int                 `yaml:"failureThreshold"`
	FailureWindow    string              `yaml:"failureWindow"`
	Actions          []HealthCheckAction `yaml:"actions"`
	Cooldown         string              `yaml:"cooldown"`
	Notify           bool                `yaml:"notify"`
}

type HealthCheckTarget struct {
	Type         string `yaml:"type"`
	Host         string `yaml:"host"`
	URL          string `yaml:"url"`
	ExpectStatus int    `yaml:"expectStatus"`
}

type HealthCheckAction struct {
	Type  string `yaml:"type"`
	Delay string `yaml:"delay"`
}

type PPPoEConfig struct {
	Username        string `yaml:"username"`
	Password        string `yaml:"password"`
	MTU             int    `yaml:"mtu"`
	MRU             int    `yaml:"mru"`
	LCPEchoInterval int    `yaml:"lcpEchoInterval"`
	LCPEchoFailure  int    `yaml:"lcpEchoFailure"`
	Persist         bool   `yaml:"persist"`
	Holdoff         int    `yaml:"holdoff"`
	IPv6CP          bool   `yaml:"ipv6cp"`
}

type USBTetherConfig struct {
	Enabled        bool   `yaml:"enabled"`
	AutoFailover   bool   `yaml:"autoFailover"`
	AutoFailback   bool   `yaml:"autoFailback"`
	FailoverDelay  string `yaml:"failoverDelay"`
	FailbackDelay  string `yaml:"failbackDelay"`
	Interface      string `yaml:"interface"`
	Metric         int    `yaml:"metric"`
	NAT            bool   `yaml:"nat"`
	TTLFix         bool   `yaml:"ttlFix"`
}

type FirewallConfig struct {
	DefaultPolicy string            `yaml:"defaultPolicy"`
	TTLFix        TTLFixConfig      `yaml:"ttlFix"`
	OpenPorts     []OpenPort        `yaml:"openPorts"`
	PortForwards  []PortForward     `yaml:"portForwards"`
	Rules         []FirewallRule    `yaml:"rules"`
	RateLimits    map[string]string `yaml:"rateLimits"`
}

type OpenPort struct {
	Name     string `yaml:"name"`
	Protocol string `yaml:"protocol"`
	Port     int    `yaml:"port"`
	Source   string `yaml:"source"`
	Enabled  bool   `yaml:"enabled"`
}

type FirewallRule struct {
	Name      string `yaml:"name"`
	Chain     string `yaml:"chain"`
	Action    string `yaml:"action"`
	SrcIP     string `yaml:"srcIP"`
	DstIP     string `yaml:"dstIP"`
	Protocol  string `yaml:"protocol"`
	Port      int    `yaml:"port"`
	Interface string `yaml:"interface"`
	Direction string `yaml:"direction"`
	Enabled   bool   `yaml:"enabled"`
	Priority  int    `yaml:"priority"`
}

type TTLFixConfig struct {
	Enabled bool `yaml:"enabled"`
	Value   int  `yaml:"value"`
}

type PortForward struct {
	Name     string `yaml:"name"`
	Protocol string `yaml:"protocol"`
	ExtPort  int    `yaml:"extPort"`
	IntIP    string `yaml:"intIP"`
	IntPort  int    `yaml:"intPort"`
	Enabled  bool   `yaml:"enabled"`
}

type QoSConfig struct {
	Enabled           bool              `yaml:"enabled"`
	Profile           string            `yaml:"profile"`
	UploadKbps        int               `yaml:"uploadKbps"`
	DownloadKbps      int               `yaml:"downloadKbps"`
	CongestionControl string            `yaml:"congestionControl"`
	PerDeviceLimits   map[string]int    `yaml:"perDeviceLimits"`
}

type DNSConfig struct {
	Upstream                []string       `yaml:"upstream"`
	DoTUpstream             string         `yaml:"dotUpstream"`
	EnableDoT               bool           `yaml:"enableDoT"`
	BlocklistURLs           []string       `yaml:"blocklistUrls"`
	BlocklistUpdateSchedule string         `yaml:"blocklistUpdateSchedule"`
	CacheSize               int            `yaml:"cacheSize"`
	QueryLog                QueryLogConfig `yaml:"queryLog"`
}

type QueryLogConfig struct {
	Enabled    bool   `yaml:"enabled"`
	LogPath    string `yaml:"logPath"`
	MaxSize    string `yaml:"maxSize"`
	Retention  string `yaml:"retention"`
	LogBlocked bool   `yaml:"logBlocked"`
}

type DHCPConfig struct {
	RangeStart   string            `yaml:"rangeStart"`
	RangeEnd     string            `yaml:"rangeEnd"`
	LeaseTime    string            `yaml:"leaseTime"`
	Gateway      string            `yaml:"gateway"`
	DNSServer    string            `yaml:"dnsServer"`
	StaticLeases []StaticLease     `yaml:"staticLeases"`
}

type StaticLease struct {
	MAC      string `yaml:"mac"`
	IP       string `yaml:"ip"`
	Hostname string `yaml:"hostname"`
}

type IPv6Config struct {
	Enabled string         `yaml:"enabled"`
	Mode    string         `yaml:"mode"`
	WAN     IPv6WANConfig  `yaml:"wan"`
	LAN     IPv6LANConfig  `yaml:"lan"`
	Privacy bool           `yaml:"privacy"`
}

type IPv6WANConfig struct {
	AcceptRA      bool   `yaml:"acceptRA"`
	RequestPrefix bool   `yaml:"requestPrefix"`
	PrefixHint    string `yaml:"prefixHint"`
}

type IPv6LANConfig struct {
	Mode       string        `yaml:"mode"`
	ULA        IPv6ULAConfig `yaml:"ula"`
	RAInterval int           `yaml:"raInterval"`
	RDNSS      bool          `yaml:"rdnss"`
}

type IPv6ULAConfig struct {
	Enabled bool   `yaml:"enabled"`
	Prefix  string `yaml:"prefix"`
}

type VPNConfig struct {
	Clients           []WGClientTunnel      `yaml:"clients"`
	Server            WGServerConfig        `yaml:"server"`
	DeviceAssignments map[string]string     `yaml:"deviceAssignments"`
}

type WGClientTunnel struct {
	Name       string `yaml:"name"`
	Endpoint   string `yaml:"endpoint"`
	PrivateKey string `yaml:"privateKey"`
	PublicKey  string `yaml:"publicKey"`
	AllowedIPs string `yaml:"allowedIPs"`
	DNS        string `yaml:"dns"`
	Table      int    `yaml:"table"`
	Fwmark     int    `yaml:"fwmark"`
}

type WGServerConfig struct {
	Enabled        bool           `yaml:"enabled"`
	ListenPort     int            `yaml:"listenPort"`
	PrivateKey     string         `yaml:"privateKey"`
	PublicKey      string         `yaml:"publicKey"`
	Address        string         `yaml:"address"`
	Address6       string         `yaml:"address6"`
	DNS            string         `yaml:"dns"`
	PostUp         string         `yaml:"postUp"`
	PostDown       string         `yaml:"postDown"`
	MTU            int            `yaml:"mtu"`
	PublicEndpoint string         `yaml:"publicEndpoint,omitempty"`
	Peers          []WGServerPeer `yaml:"peers"`
}

type WGServerPeer struct {
	Name          string   `yaml:"name"`
	PublicKey     string   `yaml:"publicKey"`
	PresharedKey  string   `yaml:"presharedKey"`
	AllowedIPs    string   `yaml:"allowedIPs"`
	Keepalive     int      `yaml:"keepalive"`
	Endpoint      string   `yaml:"endpoint,omitempty"`
	RemoteSubnets []string `yaml:"remoteSubnets,omitempty"`
	IsSiteToSite  bool     `yaml:"isSiteToSite,omitempty"`
}

type OpenVPNConfig struct {
	Clients []OVPNClientConfig `yaml:"clients"`
	Server  OVPNServerConfig   `yaml:"server"`
}

type OVPNClientConfig struct {
	Name        string `yaml:"name"`
	ConfigFile  string `yaml:"configFile,omitempty"`
	RemoteHost  string `yaml:"remoteHost,omitempty"`
	RemotePort  int    `yaml:"remotePort,omitempty"`
	Protocol    string `yaml:"protocol,omitempty"`
	Cipher      string `yaml:"cipher,omitempty"`
	Auth        string `yaml:"auth,omitempty"`
	TLSAuth     bool   `yaml:"tlsAuth,omitempty"`
	Username    string `yaml:"username,omitempty"`
	Password    string `yaml:"password,omitempty"`
	AutoConnect bool   `yaml:"autoConnect,omitempty"`
	Table       int    `yaml:"table,omitempty"`
	Fwmark      int    `yaml:"fwmark,omitempty"`
}

type OVPNServerConfig struct {
	Enabled        bool               `yaml:"enabled"`
	Protocol       string             `yaml:"protocol"`
	Port           int                `yaml:"port"`
	Device         string             `yaml:"device"`
	Subnet         string             `yaml:"subnet"`
	Subnet6        string             `yaml:"subnet6"`
	DNS            string             `yaml:"dns"`
	Cipher         string             `yaml:"cipher"`
	Auth           string             `yaml:"auth"`
	TLSAuth        bool               `yaml:"tlsAuth"`
	Compression    bool               `yaml:"compression"`
	MaxClients     int                `yaml:"maxClients"`
	Keepalive      string             `yaml:"keepalive"`
	ClientToClient bool               `yaml:"clientToClient"`
	DuplicateCN    bool               `yaml:"duplicateCn"`
	PublicEndpoint string             `yaml:"publicEndpoint,omitempty"`
	Clients        []OVPNClientEntry  `yaml:"clients"`
	CCD            map[string]string  `yaml:"ccd"`
}

type OVPNClientEntry struct {
	Name          string   `yaml:"name"`
	CommonName    string   `yaml:"commonName"`
	FixedIP       string   `yaml:"fixedIP,omitempty"`
	Enabled       bool     `yaml:"enabled"`
	IsSiteToSite  bool     `yaml:"isSiteToSite,omitempty"`
	RemoteSubnets []string `yaml:"remoteSubnets,omitempty"`
}

type RoutingConfig struct {
	Policies []RoutingPolicy `yaml:"policies"`
}

type RoutingPolicy struct {
	Name      string   `yaml:"name"`
	Enabled   bool     `yaml:"enabled"`
	Priority  int      `yaml:"priority"`
	SrcMACs   []string `yaml:"srcMacs"`
	SrcIPs    []string `yaml:"srcIps"`
	DstIPs    []string `yaml:"dstIps"`
	DstPorts  []int    `yaml:"dstPorts"`
	Domains   []string `yaml:"domains"`
	Protocol  string   `yaml:"protocol"`
	Tunnel    string   `yaml:"tunnel"`
	KillSwitch bool   `yaml:"killSwitch"`
	Schedule  string   `yaml:"schedule"`
}

type NASConfig struct {
	Shares     []ShareConfig     `yaml:"shares"`
	M3USources []M3USourceConfig `yaml:"m3uSources"`
}

type ShareConfig struct {
	Name       string   `yaml:"name"`
	Path       string   `yaml:"path"`
	GuestOK    bool     `yaml:"guestOk"`
	ReadOnly   bool     `yaml:"readOnly"`
	ValidUsers []string `yaml:"validUsers"`
}

type M3USourceConfig struct {
	URL           string   `yaml:"url"`
	DownloadPath  string   `yaml:"downloadPath"`
	Schedule      string   `yaml:"schedule"`
	IncludeGroups []string `yaml:"includeGroups"`
	ExcludeGroups []string `yaml:"excludeGroups"`
}

type SyslogConfig struct {
	Server SyslogServerConfig `yaml:"server"`
	Client SyslogClientConfig `yaml:"client"`
}

type SyslogServerConfig struct {
	Enabled      bool   `yaml:"enabled"`
	ListenUDP    string `yaml:"listenUDP"`
	ListenTCP    string `yaml:"listenTCP"`
	EnableTLS    bool   `yaml:"enableTLS"`
	LogPath      string `yaml:"logPath"`
	PerHostDirs  bool   `yaml:"perHostDirs"`
	MaxRetention string `yaml:"maxRetention"`
}

type SyslogClientConfig struct {
	Enabled    bool     `yaml:"enabled"`
	RemoteHost string   `yaml:"remoteHost"`
	Protocol   string   `yaml:"protocol"`
	EnableTLS  bool     `yaml:"enableTLS"`
	Facilities []string `yaml:"facilities"`
}

type NTPConfig struct {
	Server NTPServerConfig `yaml:"server"`
	Client NTPClientConfig `yaml:"client"`
	RTCSync      bool     `yaml:"rtcSync"`
	AllowSubnets []string `yaml:"allowSubnets"`
}

type NTPServerConfig struct {
	Enabled       bool   `yaml:"enabled"`
	ListenAddress string `yaml:"listenAddress"`
	ListenPort    int    `yaml:"listenPort"`
}

type NTPClientConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Sources  []string `yaml:"sources"`
	Fallback string   `yaml:"fallback"`
}

type StorageConfig struct {
	RAID               RAIDConfig `yaml:"raid"`
	SMARTCheckInterval int        `yaml:"smartCheckInterval"`
}

type RAIDConfig struct {
	Device     string   `yaml:"device"`
	Level      int      `yaml:"level"`
	Members    []string `yaml:"members"`
	MountPoint string   `yaml:"mountPoint"`
}

var configMu sync.RWMutex

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return cfg, nil
}

func Save(path string, cfg *Config) error {
	configMu.Lock()
	defer configMu.Unlock()

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return atomicWrite(path, data)
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("fsync temp: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp to config: %w", err)
	}

	return nil
}
