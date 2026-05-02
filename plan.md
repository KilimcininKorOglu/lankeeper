# Home Router Software — Implementation Plan (Go + HTMX)

## Context

Turkcell Superonline'ın ISP modemleri bufferbloat sorununa neden oluyor ve 1 Gbps bağlantıda SQM/QoS desteği sunmuyor. Mevcut ZTE modem yerine Intel i5 3470 tabanlı özel donanım üzerine sıfırdan router yazılımı geliştirilecek. Hedef: PPPoE WAN bağlantısı, nftables firewall, WireGuard VPN, Samba NAS ve web dashboard'u tek bir Go binary'sinde birleştirmek.

## Kurallar

1. **Her deği��iklikte commit atılır.** Fonksiyonel bir birim tamamlandığında hemen commit.
2. **Asla yama yapılmaz.** Sorunun kök nedeni bulunur ve oradan çöz��lür.
3. **Çoklu dil desteği (i18n) ilk günden zorunludur.** Tüm UI metinleri locale JSON dosyalarından gelir, template'lere sabit metin yazılmaz.

## Neden Go + HTMX?

| Kriter             | Python + FastAPI + Vanilla JS       | Go + HTMX                              |
|--------------------|-------------------------------------|-----------------------------------------|
| Deployment         | venv + pip + uvicorn + systemd      | Tek statik binary, `scp` ile deploy     |
| Bellek             | ~80-120 MB (Python runtime + deps)  | ~10-20 MB (compiled binary)             |
| Startup            | 2-5 saniye (import + uvicorn)       | <100ms                                  |
| Concurrency        | asyncio (single-threaded event loop)| goroutine (lightweight threads, multi-core) |
| Frontend           | Client-side SPA, JS state yönetimi  | Server-side HTML, HTMX partial swap     |
| Type safety        | Runtime (Pydantic)                  | Compile-time (structs)                  |
| Bağımlılık         | ~12 pip paketi                      | stdlib + 4-5 Go modülü                  |
| Router için uyum   | Orta (GC pauses, memory overhead)   | Yüksek (düşük latency, düşük bellek)   |

## Current State

- Proje dizini boş — sıfırdan (greenfield) geliştirme
- Donanım hazır: 2x Gigabit NIC, RAID-1 depolama, Debian 12 Bookworm (minimal)

## What We're NOT Doing

- IPv6 desteği (v1 kapsamı dışı — `ip6tables -P FORWARD DROP` ile kapatılacak)
- Wi-Fi yönetimi (kullanıcı ayrı AP'ler kullanıyor)
- Harici DNS/DHCP web UI (Pi-hole, AdGuard Home) — Unbound + dnsmasq doğrudan Go'dan yönetilecek
- Veritabanı (tüm config YAML dosyalarında)
- JavaScript framework (React/Vue/Svelte yok — HTMX + server-side rendering)
- Çoklu ISP / failover (tek PPPoE bağlantı)
- Konteyner/Docker desteği
- ORM veya SQL — dosya tabanlı config

---

## Mimari Kararlar

### 1. Tek Binary, İki Mod

Python'daki iki ayrı process (agent + web) yerine, Go'da **tek binary iki modda** çalışır:

```
home-router
├── home-router serve    → Web sunucu (unprivileged, capability: CAP_NET_BIND_SERVICE)
└── home-router agent    → Privileged agent (root, UDS listener)
```

```
┌─────────────────────────────┐     ┌──────────────────────────────┐
│  home-router serve          │     │  home-router agent           │
│  User: homerouter           │────▶│  User: root                  │
│  net/http + HTMX            │ UDS │  Unix Socket IPC             │
│  Port 8443 (LAN only)       │     │  Op Whitelist Dispatcher     │
│  SSE for real-time updates   │     │  goroutine per operation     │
└─────────────────────────────┘     └──────────────────────────────┘
        │                                      │
        ▼                                      ▼
   html/template → HTMX partials    nftables, pppd, wg, tc,
   SSE event stream                 ip rule/route, smartctl
```

- **Web process** (unprivileged) asla `exec.Command` ile root komut çalıştırmaz
- **Agent process** (root) strict op whitelist ile yalnızca bilinen işlemleri yürütür
- IPC: Unix domain socket (`/run/home-router/agent.sock`) + JSON-RPC 2.0
- Tek binary: `go build -o home-router ./cmd/home-router`

### 2. HTMX + Server-Side Rendering

SPA yerine **hypermedia-driven** yaklaşım:

```
Tarayıcı                          Go Sunucu
   │                                  │
   │─── GET / ───────────────────────▶│ → tam sayfa HTML render
   │◀── full HTML + HTMX attrs ──────│
   │                                  │
   │─── hx-get="/partials/stats" ───▶│ → sadece <div> fragment render
   │◀── HTML fragment ───────────────│ → HTMX swap: innerHTML
   │                                  │
   │─── SSE /events/stats ──────────▶│ → goroutine: 1s interval
   │◀── data: <html fragment> ───────│ → HTMX SSE swap
```

- İlk yükleme: tam sayfa HTML (`html/template`)
- Etkileşimler: HTMX ile partial HTML swap (`hx-get`, `hx-post`, `hx-swap`)
- Real-time: SSE (Server-Sent Events) ile dashboard metrikleri
- Drag-and-drop: HTMX Sortable extension + `hx-trigger="drop"`
- JS minimal: sadece chart (Canvas API) ve drag-drop için küçük helper'lar
- Tema: CSS custom properties + `prefers-color-scheme`

### 3. Internationalization (i18n) — İlk Günden

Tüm UI metinleri JSON locale dosyalarından yüklenir. Template'lere sabit metin yazılmaz.

**Desteklenen diller:** Türkçe (`tr`), İngilizce (`en`)

```
web/locales/
├── tr.json    # Türkçe (varsayılan)
└── en.json    # İngilizce
```

**JSON yapısı** — nokta-ayrılmış düz anahtar (flat keys, nested değil):

```json
{
    "nav.dashboard": "Dashboard",
    "nav.network": "Network",
    "nav.firewall": "Firewall",
    "nav.vpn": "VPN",
    "nav.dns": "DNS",
    "nav.qos": "QoS",
    "nav.nas": "NAS",
    "nav.storage": "Storage",
    "nav.settings": "Settings",
    "dashboard.title": "Dashboard",
    "dashboard.uptime": "Uptime",
    "dashboard.wanIp": "WAN IP",
    "dashboard.activeDevices": "Active Devices",
    "dashboard.cpuUsage": "CPU Usage",
    "dashboard.ramUsage": "RAM Usage",
    "dashboard.download": "Download",
    "dashboard.upload": "Upload",
    "pppoe.connect": "Connect",
    "pppoe.disconnect": "Disconnect",
    "pppoe.status.connected": "Connected",
    "pppoe.status.disconnected": "Disconnected",
    "pppoe.confirmConnect": "Start PPPoE connection?",
    "firewall.title": "Firewall Rules",
    "firewall.addRule": "Add Rule",
    "firewall.portForward": "Port Forward",
    "firewall.watchdogConfirm": "New rules applied. Will be reverted in 30 seconds if not confirmed.",
    "firewall.confirm": "Confirm",
    "vpn.title": "VPN Management",
    "vpn.addTunnel": "Add Tunnel",
    "vpn.assignDevice": "Drag device to assign to VPN",
    "vpn.unassigned": "Unassigned Devices",
    "common.save": "Save",
    "common.cancel": "Cancel",
    "common.delete": "Delete",
    "common.confirm": "Confirm",
    "common.loading": "Loading...",
    "common.success": "Operation successful",
    "common.error": "An error occurred",
    "auth.login": "Login",
    "auth.password": "Password",
    "auth.wrongPassword": "Invalid password",
    "auth.logout": "Logout"
}
```

**Go i18n paketi:**

```go
// internal/i18n/i18n.go
package i18n

type Locale struct {
    Code     string            // "tr", "en"
    Messages map[string]string // flat key → translated string
}

type I18n struct {
    locales  map[string]*Locale
    fallback string // "tr"
}

func (i *I18n) T(lang, key string) string // döndür: messages[key] veya fallback
func (i *I18n) WithParams(lang, key string, params map[string]string) string // parametreli: "Hoş geldin, {{name}}"
```

**Template'lerde kullanım:**

```html
<!-- Her template'te .Lang context'ten gelir -->
<h1>{{ t .Lang "dashboard.title" }}</h1>
<button hx-post="/pppoe/connect"
        hx-confirm="{{ t .Lang "pppoe.confirmConnect" }}">
    {{ t .Lang "pppoe.connect" }}
</button>

<!-- Parametreli çeviri -->
<p>{{ tp .Lang "dashboard.connectedFor" "duration" .Uptime }}</p>
```

**Dil tespiti sırası:**
1. `lang` cookie (kullanıcı tercihi)
2. `Accept-Language` header
3. Varsayılan: `tr`

**Dil değiştirme:**
```html
<!-- sidebar veya settings'te -->
<div class="lang-switch">
    <button hx-post="/settings/lang" hx-vals='{"lang":"tr"}'
            class="{{ if eq .Lang "tr" }}active{{ end }}">TR</button>
    <button hx-post="/settings/lang" hx-vals='{"lang":"en"}'
            class="{{ if eq .Lang "en" }}active{{ end }}">EN</button>
</div>
```

`POST /settings/lang` → `lang` cookie set → `HX-Refresh: true` header → tam sayfa yenileme.

### 4. Atomic Network Changes (eski 3)

```go
func (s *FirewallService) Apply(ctx context.Context, rules *NftRuleset) error {
    txn := NewAtomicChange("firewall")
    defer txn.Rollback() // hata olursa otomatik rollback

    if err := txn.Snapshot(); err != nil {  // nft list ruleset > backup
        return err
    }
    if err := txn.Validate(rules); err != nil { // nft -c -f (dry-run)
        return err
    }
    if err := txn.Apply(rules); err != nil { // nft -f
        return err
    }

    txn.StartWatchdog(30 * time.Second) // 30s onay bekleme
    txn.Commit() // rollback iptal
    return nil
}
```

Agent'ta 30 saniyelik watchdog: apply sonrası web'den onay gelmezse otomatik rollback.

### 5. VPN Policy Routing

```
nftables fwmark (kaynak IP'ye göre) → ip rule fwmark X lookup table_wgN → per-table default route
ct mark ile reply paketlerde fwmark korunur
```

### 6. DNS + DHCP: Unbound + dnsmasq

İki ayrı servis, her biri tek bir iş yapar:

- **Unbound** — Recursive DNS resolver. ISP DNS'ine bağımlılık yok, root sunuculardan doğrudan çözer. Reklam engelleme: blocklist dosyası ile (`local-zone: "ads.example.com" always_refuse`). DNS-over-TLS upstream desteği.
- **dnsmasq** — Yalnızca DHCP sunucu. DNS forwarding kapalı (`port=0`), DHCP lease yönetimi, statik lease ataması.

Her iki servis de Go'dan config dosyası ile yönetilir (`text/template` → config render → `SIGHUP` reload). REST API yok — doğrudan config dosyası + lease dosyası parse.

```
İstemci DNS sorgusu → Unbound (:53) → recursive resolution / blocklist
İstemci DHCP isteği → dnsmasq (:67) → IP ata, lease kaydet
Go Web UI → config dosyası oluştur → SIGHUP ile reload
Go Web UI → /var/lib/misc/dnsmasq.leases parse → lease tablosu
Go Web UI → unbound-control stats → DNS istatistikleri
```

### 7. Config Yönetimi

```go
// YAML config → Go struct (compile-time type safety)
type Config struct {
    System     SystemConfig     `yaml:"system"`
    Interfaces InterfaceConfig  `yaml:"interfaces"`
    PPPoE      PPPoEConfig      `yaml:"pppoe"`
    Firewall   FirewallConfig   `yaml:"firewall"`
    QoS        QoSConfig        `yaml:"qos"`
    DNS        DNSConfig        `yaml:"dns"`
    DHCP       DHCPConfig       `yaml:"dhcp"`
    VPN        VPNConfig        `yaml:"vpn"`
    NAS        NASConfig        `yaml:"nas"`
    Storage    StorageConfig    `yaml:"storage"`
}
```

- Atomic write: tmp → fsync → rename
- Credentials: AES-256-GCM ile şifreleme (Go `crypto/aes` + `crypto/cipher`)
- Validation: struct tag'ler + custom validator fonksiyonlar

---

## Dizin Yapısı

```
home-router/
├── cmd/
│   └── home-router/
│       └── main.go               # CLI entry point (serve | agent)
├── internal/
│   ├── agent/
│   │   ├── server.go             # Root agent — UDS listener, op dispatcher
│   │   ├── client.go             # Web'den agent'a IPC istemcisi
│   │   ├── operations.go         # İzin verilen op tanımları + registry
│   │   └── watchdog.go           # Rollback watchdog timer (goroutine)
│   ├── config/
│   │   ├── config.go             # YAML load/save, struct tanımları
│   │   ├── crypto.go             # AES-256-GCM encrypt/decrypt
│   │   ├── defaults.go           # Varsayılan config değerleri
│   │   └── validate.go           # Config doğrulama
│   ├── web/
│   │   ├── server.go             # HTTP sunucu setup, middleware chain
│   │   ├── middleware.go         # Auth, CSRF, rate limit, LAN-only
│   │   ├── auth.go               # Login/logout, session/cookie, bcrypt
│   │   ├── sse.go                # SSE broker (real-time stats broadcast)
│   │   └── handlers/
│   │       ├── dashboard.go      # GET / → dashboard sayfası
│   │       ├── network.go        # Interface bilgileri
│   │       ├── pppoe.go          # WAN bağlantı yönetimi
│   │       ├── firewall.go       # nftables kuralları
│   │       ├── dns.go            # Unbound DNS yönetimi + istatistik
│   │       ├── dhcp.go           # dnsmasq DHCP lease yönetimi
│   │       ├── qos.go            # SQM/QoS profilleri
│   │       ├── vpn.go            # WireGuard tünelleri + cihaz ataması
│   │       ├── nas.go            # Samba paylaşımları
│   │       ├── storage.go        # RAID durumu, disk sağlığı
│   │       └── system.go         # Ayarlar, yedekleme, reboot
│   ├── services/
│   │   ├── pppoe.go              # pppd yönetimi
│   │   ├── firewall.go           # nftables ruleset oluşturma + uygulama
│   │   ├── dns.go                # Unbound config yönetimi + blocklist + unbound-control
│   │   ├── dhcp.go               # dnsmasq config yönetimi + lease parse
│   │   ├── qos.go                # tc + CAKE qdisc yönetimi
│   │   ├── vpn.go                # WireGuard tunnel + policy routing
│   │   ├── nas.go                # Samba config + M3U parser
│   │   ├── storage.go            # mdadm + smartctl
│   │   ├── monitor.go            # Sistem istatistikleri toplayıcı (goroutine)
│   │   └── backup.go             # Config export/import
│   ├── i18n/
│   │   ├── i18n.go               # Locale yükleme, T() ve WithParams() fonksiyonları
│   │   └── middleware.go         # Dil tespiti middleware (cookie → Accept-Language → default)
│   ├── netutil/
│   │   ├── atomic.go             # AtomicChange struct + rollback logic
│   │   ├── exec.go               # Güvenli exec.Command wrapper
│   │   ├── iface.go              # Interface bilgisi okuma (/proc/net/dev)
│   │   └── validate.go           # IP, CIDR, MAC, port doğrulama
│   └── tmpl/
│       ├── render.go             # Template rendering helper'ları + i18n entegrasyonu
│       └── funcs.go              # Template fonksiyonları (t, tp, formatBytes, humanTime, ...)
├── web/
│   ├── templates/
│   │   ├── layouts/
│   │   │   ├── base.html         # Ana layout (sidebar + content area)
│   │   │   └── auth.html         # Login layout (sidebar'sız)
│   │   ├── pages/
│   │   │   ├── dashboard.html    # Dashboard tam sayfa
│   │   │   ├── network.html
│   │   │   ├── firewall.html
│   │   │   ├── vpn.html
│   │   │   ├── dns.html
│   │   │   ├── dhcp.html
│   │   │   ├── qos.html
│   │   │   ├── nas.html
│   │   │   ├── storage.html
│   │   │   ├── settings.html
│   │   │   └── login.html
│   │   └── partials/
│   │       ├── sidebar.html      # Sidebar navigasyon
│   │       ├── stats_card.html   # Dashboard stat kartı (SSE ile güncellenir)
│   │       ├── bandwidth.html    # Bandwidth grafiği container
│   │       ├── lease_table.html  # DHCP lease tablosu (HTMX swap)
│   │       ├── fw_rules.html     # Firewall kural listesi
│   │       ├── vpn_panel.html    # VPN cihaz atama paneli
│   │       ├── vpn_device.html   # Tekil cihaz kartı (draggable)
│   │       ├── share_list.html   # Samba paylaşım listesi
│   │       ├── raid_status.html  # RAID durumu
│   │       ├── toast.html        # Toast notification
│   │       └── confirm.html      # Onay dialog
│   ├── static/
│   │   ├── css/
│   │   │   ├── reset.css
│   │   │   ├── variables.css     # CSS custom properties (dark/light tema)
│   │   │   ├── layout.css
│   │   │   ├── components.css
│   │   │   └── pages.css
│   │   ├── js/
│   │   │   ├── htmx.min.js      # HTMX library (~14KB gzip)
│   │   │   ├── htmx-sse.js      # HTMX SSE extension
│   │   │   ├── htmx-sortable.js # HTMX Sortable extension (drag-drop)
│   │   │   ├── chart.js         # Canvas-based grafik helper (minimal, custom)
│   │   │   └── app.js           # Tema toggle, chart init (~50 satır)
│   │   └── icons/               # SVG ikonlar (inline veya sprite)
│   ├── locales/
│   │   ├── tr.json               # Türkçe çeviriler (varsayılan dil)
│   │   └── en.json               # İngilizce çeviriler
│   └── embed.go                  # go:embed ile static + template + locale'leri binary'ye göm
├── configs/
│   ├── sysconf/                  # Sistem config şablonları
│   │   ├── nftables.conf.tmpl    # nftables ruleset (Go text/template)
│   │   ├── pppoe-peer.tmpl       # /etc/ppp/peers/wan
│   │   ├── pppoe-options.tmpl    # pppd seçenekleri
│   │   ├── unbound.conf.tmpl     # Unbound recursive DNS config
│   │   ├── dnsmasq.conf.tmpl    # dnsmasq DHCP-only config
│   │   ├── wireguard.conf.tmpl  # WireGuard interface config
│   │   └── smb.conf.tmpl         # Samba paylaşım config
│   └── defaults/
│       ├── router.yaml           # Varsayılan ana config
│       ├── firewall.yaml         # Varsayılan firewall kuralları
│       ├── qos.yaml              # Varsayılan QoS profilleri
│       ├── vpn.yaml              # Boş VPN config
│       └── nas.yaml              # Boş NAS config
├── deploy/
│   ├── systemd/
│   │   ├── home-router.target    # Orchestration target
│   │   ├── home-router-agent.service
│   │   └── home-router-web.service
│   ├── install.sh                # Tam kurulum scripti
│   ├── setup-interfaces.sh       # udev kuralları + NIC isimlendirme
│   ├── factory-reset.sh          # Fabrika ayarlarına dönüş
│   └── backup.sh                 # Cron backup scripti
├── go.mod
├── go.sum
├── Makefile                      # build, test, lint, deploy, cross-compile
├── .goreleaser.yaml              # Release automation (opsiyonel)
└── README.md
```

### `go:embed` ile Tek Binary

```go
// web/embed.go
package web

import "embed"

//go:embed templates/* static/* locales/*
var EmbeddedFS embed.FS
```

Tüm HTML template'leri, CSS, JS, ikonlar ve locale JSON dosyaları binary'nin içine gömülür. Deploy = tek dosya kopyala.

---

## Config Schema (router.yaml)

```yaml
system:
  hostname: "home-router"
  timezone: "Europe/Istanbul"
  language: "tr"                         # tr | en (varsayılan dil, cookie override eder)
  adminPasswordHash: "$2a$12$..."       # bcrypt
  sessionSecret: "..."                   # 32-byte hex, cookie signing
  webPort: 8443
  webBind: "10.0.0.1"                   # Sadece LAN

interfaces:
  wan:
    device: "enp3s0"                     # udev rule ile sabitlenmiş
    type: "pppoe"
    mtu: 1492
  lan:
    device: "enp0s25"
    address: "10.0.0.1/24"
    mtu: 1500

pppoe:
  username: "..."                        # .credentials.enc'den okunur
  password: "..."
  mtu: 1492
  mru: 1492
  lcpEchoInterval: 10
  lcpEchoFailure: 3
  persist: true
  holdoff: 5

firewall:
  defaultPolicy: "drop"                 # WAN input/forward
  portForwards: []
  rateLimits:
    ssh: "3/minute"
    web: "30/minute"

qos:
  enabled: true
  profile: "cake"                        # cake | fq_codel | none
  uploadKbps: 40000
  downloadKbps: 950000
  congestionControl: "bbr"               # bbr | cubic
  perDeviceLimits: {}

dns:
  upstream: []                           # boş = recursive (root hints), dolu = forwarder
  dotUpstream: "1.1.1.1@853"             # DNS-over-TLS upstream (opsiyonel)
  enableDoT: false
  blocklistUrls:
    - "https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts"
  blocklistUpdateSchedule: "0 3 * * *"   # Her gün 03:00
  cacheSize: 50000                       # Unbound msg-cache-size entry sayısı
  logQueries: true

dhcp:
  rangeStart: "10.0.0.100"
  rangeEnd: "10.0.0.250"
  leaseTime: "12h"
  gateway: "10.0.0.1"
  dnsServer: "10.0.0.1"                  # Unbound'a yönlendir
  staticLeases:
    - mac: "aa:bb:cc:dd:ee:ff"
      ip: "10.0.0.10"
      hostname: "desktop"

vpn:
  tunnels:
    - name: "nl-amsterdam"
      endpoint: "1.2.3.4:51820"
      privateKey: "..."                  # .credentials.enc
      publicKey: "..."
      allowedIPs: "0.0.0.0/0"
      dns: "10.0.0.1"
      table: 100
      fwmark: 100
  deviceAssignments:
    "aa:bb:cc:dd:ee:ff": "nl-amsterdam"

nas:
  shares:
    - name: "media"
      path: "/mnt/raid/media"
      guestOk: true
      readOnly: true
    - name: "backups"
      path: "/mnt/raid/backups"
      guestOk: false
      validUsers: ["admin"]
  m3uSources:
    - url: "http://example.com/playlist.m3u"
      downloadPath: "/mnt/raid/media/iptv"
      schedule: "0 4 * * *"

storage:
  raid:
    device: "/dev/md0"
    level: 1
    members: ["/dev/sda1", "/dev/sdb1"]
  smartCheckInterval: 3600
```

---

## Route + Handler Inventory

Go'da HTMX ile iki tür endpoint var: **sayfa** (tam HTML) ve **partial** (HTML fragment).

### Auth + i18n
| Method | Path               | Tür     | Açıklama                              |
|--------|--------------------|---------|---------------------------------------|
| GET    | /login             | Sayfa   | Login formu render                    |
| POST   | /login             | Partial | Oturum aç → cookie set → redirect    |
| POST   | /logout            | Partial | Oturum kapat → cookie clear → redirect|
| POST   | /settings/lang     | Partial | Dil değiştir → lang cookie → HX-Refresh |

### Dashboard
| Method | Path                    | Tür     | Açıklama                          |
|--------|-------------------------|---------|------------------------------------|
| GET    | /                       | Sayfa   | Dashboard tam sayfa                |
| GET    | /partials/stats         | Partial | Stat kartları (HTMX poll/SSE)     |
| GET    | /events/stats           | SSE     | Real-time sistem metrikleri stream |

### Network / PPPoE
| Method | Path                    | Tür     | Açıklama                          |
|--------|-------------------------|---------|------------------------------------|
| GET    | /network                | Sayfa   | Ağ ayarları sayfası                |
| GET    | /partials/wan-status    | Partial | WAN durum kartı                   |
| POST   | /pppoe/connect          | Partial | PPPoE bağlantısını başlat          |
| POST   | /pppoe/disconnect       | Partial | PPPoE bağlantısını kes             |
| PUT    | /pppoe/config           | Partial | PPPoE ayarlarını güncelle          |

### Firewall
| Method | Path                        | Tür     | Açıklama                      |
|--------|-----------------------------|---------|--------------------------------|
| GET    | /firewall                   | Sayfa   | Firewall kuralları sayfası     |
| GET    | /partials/fw-rules          | Partial | Kural listesi (HTMX swap)     |
| POST   | /firewall/port-forward      | Partial | Port yönlendirme ekle          |
| DELETE | /firewall/port-forward/{id} | Partial | Port yönlendirme sil           |
| POST   | /firewall/confirm           | Partial | Watchdog onay (30s timeout)    |

### DNS (Unbound)
| Method | Path                       | Tür     | Açıklama                          |
|--------|----------------------------|---------|------------------------------------|
| GET    | /dns                       | Sayfa   | DNS ayarları + istatistikler       |
| GET    | /partials/dns-stats        | Partial | DNS cache/query istatistikleri     |
| PUT    | /dns/config                | Partial | DNS ayarlarını güncelle            |
| POST   | /dns/blocklist/update      | Partial | Blocklist'i şimdi güncelle         |
| GET    | /partials/dns-blocklist    | Partial | Blocklist durumu + kaynak listesi  |

### DHCP (dnsmasq)
| Method | Path                       | Tür     | Açıklama                          |
|--------|----------------------------|---------|------------------------------------|
| GET    | /dhcp                      | Sayfa   | DHCP lease listesi + ayarlar       |
| GET    | /partials/leases           | Partial | Aktif lease tablosu                |
| POST   | /dhcp/lease                | Partial | Statik lease ekle                  |
| DELETE | /dhcp/lease/{mac}          | Partial | Statik lease sil                   |
| PUT    | /dhcp/config               | Partial | DHCP aralık/süre ayarları          |

### QoS
| Method | Path                    | Tür     | Açıklama                          |
|--------|-------------------------|---------|------------------------------------|
| GET    | /qos                    | Sayfa   | QoS ayarları sayfası               |
| GET    | /partials/qos-status    | Partial | Aktif QoS profili + istatistik     |
| PUT    | /qos/profile            | Partial | Profil değiştir                    |
| PUT    | /qos/limits             | Partial | Bant genişliği limitleri           |
| PUT    | /qos/congestion         | Partial | Congestion control (BBR/CUBIC)     |

### VPN
| Method | Path                        | Tür     | Açıklama                      |
|--------|-----------------------------|---------|--------------------------------|
| GET    | /vpn                        | Sayfa   | VPN yönetimi sayfası           |
| GET    | /partials/vpn-panel         | Partial | Tünel + cihaz paneli           |
| POST   | /vpn/tunnel                 | Partial | Yeni tünel ekle                |
| DELETE | /vpn/tunnel/{name}          | Partial | Tünel sil                      |
| PUT    | /vpn/assign                 | Partial | Cihaz-tünel ataması (drag-drop)|
| DELETE | /vpn/unassign/{mac}         | Partial | Cihaz atamasını kaldır         |

### NAS
| Method | Path                    | Tür     | Açıklama                          |
|--------|-------------------------|---------|------------------------------------|
| GET    | /nas                    | Sayfa   | NAS yönetimi sayfası               |
| GET    | /partials/shares        | Partial | Paylaşım listesi                  |
| POST   | /nas/share              | Partial | Yeni paylaşım ekle                |
| PUT    | /nas/share/{name}       | Partial | Paylaşım güncelle                 |
| DELETE | /nas/share/{name}       | Partial | Paylaşım sil                      |
| POST   | /nas/m3u/sync           | Partial | M3U senkronizasyonu başlat         |
| GET    | /partials/m3u-status    | Partial | M3U senkronizasyon durumu          |

### Storage
| Method | Path                    | Tür     | Açıklama                          |
|--------|-------------------------|---------|------------------------------------|
| GET    | /storage                | Sayfa   | Depolama sayfası                   |
| GET    | /partials/raid          | Partial | RAID durumu                        |
| GET    | /partials/smart         | Partial | Disk sağlık bilgileri              |
| GET    | /partials/disk-usage    | Partial | Disk kullanımı                     |

### System
| Method | Path                    | Tür     | Açıklama                          |
|--------|-------------------------|---------|------------------------------------|
| GET    | /settings               | Sayfa   | Sistem ayarları                    |
| PUT    | /settings/system        | Partial | Hostname, timezone güncelle        |
| PUT    | /settings/password      | Partial | Şifre değiştir                     |
| POST   | /system/reboot          | Partial | Sistemi yeniden başlat             |
| GET    | /partials/logs          | Partial | journalctl çıktısı (paginated)    |
| POST   | /backup/export          | Dosya   | Config dışa aktar (.tar.gz)       |
| POST   | /backup/import          | Partial | Config içe aktar                   |

---

## Go Bağımlılıkları (go.mod)

```
module github.com/user/home-router

go 1.23

require (
    gopkg.in/yaml.v3 v3.0.1         // Config YAML parse
    golang.org/x/crypto v0.31.0      // bcrypt, AES-GCM
    github.com/gorilla/sessions v1.4.0 // Cookie-based session
)
```

**Bilinçli olarak kullanılMAyacaklar:**
- HTTP router framework yok — `net/http.ServeMux` (Go 1.22+ method routing)
- ORM yok — dosya tabanlı config
- Template engine yok — `html/template` (stdlib)
- WebSocket yok — SSE (çok daha basit, HTMX native desteği)
- JSON API yok — HTML partial'lar döner (HTMX paradigması)

**Toplam harici bağımlılık: 3 modül.** Geri kalan her şey Go stdlib.

## Sistem Gereksinimleri (install.sh)

```bash
apt install -y \
    ppp pppoe \
    nftables \
    wireguard-tools \
    samba samba-common-bin \
    smartmontools mdadm \
    iproute2 \
    unbound \
    dnsmasq

# dnsmasq: DNS kapalı (port=0), sadece DHCP
# unbound: recursive DNS resolver + blocklist
# Go sadece build makinede gerekli, hedef makinede gerekli DEĞİL
```

## Build & Deploy

```makefile
# Makefile
BINARY  := home-router
VERSION := $(shell git describe --tags --always)
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/home-router

test:
	go test ./... -race -count=1

lint:
	golangci-lint run

deploy: build
	scp $(BINARY) router:/opt/home-router/
	ssh router "systemctl restart home-router.target"
```

---

## HTMX Etkileşim Örnekleri

### Dashboard Stat Kartı (SSE ile real-time)
```html
<!-- base.html layout'ta SSE bağlantısı -->
<div hx-ext="sse" sse-connect="/events/stats">
    <div id="stats-cards" sse-swap="stats" hx-swap="innerHTML">
        {{ template "partials/stats_card.html" . }}
    </div>
</div>
```

### PPPoE Bağlantı Butonu (i18n)
```html
<button hx-post="/pppoe/connect"
        hx-target="#wan-status"
        hx-swap="outerHTML"
        hx-confirm="{{ t .Lang "pppoe.confirmConnect" }}"
        hx-indicator="#wan-spinner">
    {{ t .Lang "pppoe.connect" }}
</button>
<div id="wan-status">
    {{ template "partials/wan-status.html" . }}
</div>
```

### VPN Drag-and-Drop Cihaz Ataması (i18n)
```html
<!-- Cihaz listesi (sol panel) -->
<h3>{{ t .Lang "vpn.unassigned" }}</h3>
<div id="unassigned-devices" class="device-pool">
    {{ range .UnassignedDevices }}
    <div class="device-card" draggable="true"
         data-mac="{{ .MAC }}">
        <span>{{ .Hostname }}</span>
        <small>{{ .IP }}</small>
    </div>
    {{ end }}
</div>

<!-- VPN tünel drop zone (sağ panel) -->
{{ range .Tunnels }}
<div class="vpn-tunnel-zone"
     data-tunnel="{{ .Name }}"
     hx-put="/vpn/assign"
     hx-target="#vpn-panel"
     hx-swap="outerHTML"
     hx-trigger="drop"
     hx-vals='js:{"mac": event.dataTransfer.getData("text/mac"), "tunnel": "{{ .Name }}"}'>
    <h3>{{ .Name }}</h3>
    <p class="drop-hint">{{ t $.Lang "vpn.assignDevice" }}</p>
    {{ range .AssignedDevices }}
        {{ template "partials/vpn_device.html" $ }}
    {{ end }}
</div>
{{ end }}
```

### Firewall Watchdog Onay (i18n)
```html
<div id="fw-confirm" class="confirm-banner"
     hx-post="/firewall/confirm"
     hx-trigger="click"
     hx-swap="outerHTML">
    <p>{{ t .Lang "firewall.watchdogConfirm" }}</p>
    <div class="countdown" data-seconds="30"></div>
    <button>{{ t .Lang "firewall.confirm" }}</button>
</div>
```

### Dil Değiştirme (Sidebar)
```html
<div class="lang-switch">
    <button hx-post="/settings/lang" hx-vals='{"lang":"tr"}'
            class="{{ if eq .Lang "tr" }}active{{ end }}">TR</button>
    <button hx-post="/settings/lang" hx-vals='{"lang":"en"}'
            class="{{ if eq .Lang "en" }}active{{ end }}">EN</button>
</div>
```

---

## Implementation Phases

### Phase 1: Proje İskeleti + Agent IPC + i18n Altyapısı (3 gün)
**Hedef:** Go module, CLI skeleton, privilege-separated agent/web mimarisi, UDS IPC, i18n çekirdek paketi.

Oluşturulacak dosyalar:
- `go.mod`, `go.sum`
- `Makefile`
- `cmd/home-router/main.go` — CLI: `serve` ve `agent` subcommand'ları
- `internal/agent/server.go` — Root agent: UDS dinleyici, JSON-RPC 2.0 dispatcher
- `internal/agent/client.go` — Agent IPC client (web'den kullanılır)
- `internal/agent/operations.go` — Op whitelist registry
- `internal/config/config.go` — YAML config struct + load/save
- `internal/config/crypto.go` — AES-256-GCM encrypt/decrypt
- `internal/config/defaults.go` — Varsayılan config
- `internal/config/validate.go` — Config doğrulama
- `internal/i18n/i18n.go` — Locale yükleme, T(), WithParams()
- `internal/i18n/middleware.go` — Dil tespiti middleware (cookie → Accept-Language → default)
- `internal/netutil/atomic.go` — AtomicChange struct
- `internal/netutil/exec.go` — Güvenli exec.Command wrapper
- `web/locales/tr.json` — Türkçe çeviriler (tüm anahtarlar)
- `web/locales/en.json` — İngilizce çeviriler (tüm anahtarlar)
- `configs/defaults/router.yaml` — Varsayılan config dosyası
- `deploy/systemd/home-router-agent.service`
- `deploy/systemd/home-router-web.service`
- `deploy/systemd/home-router.target`
- `deploy/install.sh`

Adımlar:
1. `go mod init`, Makefile (build/test/lint)
2. CLI: `cobra` kullanmadan stdlib `flag` + subcommand dispatch
3. Config: YAML struct (`Language` field dahil), atomic file write (tmp→fsync→rename)
4. AES-256-GCM: credential encrypt/decrypt (Go `crypto/aes` + `crypto/cipher`)
5. **i18n paketi:** JSON locale dosyalarını `embed.FS`'den yükle, `T(lang, key)` ve `WithParams(lang, key, params)` fonksiyonları
6. **i18n middleware:** request'ten dil tespit et (cookie → Accept-Language → config default), `context.WithValue` ile handler'lara ilet
7. **Locale JSON dosyaları:** `tr.json` ve `en.json` — tüm UI anahtarları (nav, dashboard, pppoe, firewall, vpn, qos, nas, storage, settings, common, auth)
8. Agent server: `net.Listen("unix", socketPath)` + goroutine per connection
9. JSON-RPC 2.0 protocol: `{"method": "pppoe.connect", "params": {...}, "id": 1}`
10. Agent client: dial UDS, send request, read response, timeout
11. Op whitelist: yalnızca kayıtlı method'lar çalışır
12. systemd unit dosyaları + install.sh
13. Unit test: agent IPC round-trip + i18n T() fonksiyonu + eksik anahtar fallback

Manuel doğrulama:
- `go build ./...` hatasız derleniyor mu
- `go test ./... -race` geçiyor mu
- Agent socket test: JSON-RPC ping/pong
- i18n test: `T("tr", "nav.dashboard")` → `"Gösterge Paneli"`, `T("en", "nav.dashboard")` → `"Dashboard"`
- Eksik anahtar: `T("en", "nonexistent.key")` → fallback `"tr"` dili → bulamazsa key'i döndür

### Phase 2: Web Sunucu + Auth + HTMX Layout + i18n Entegrasyonu (3 gün)
**Hedef:** HTTP sunucu, session auth, HTMX base layout, login sayfası, middleware chain, i18n template entegrasyonu.

Oluşturulacak dosyalar:
- `internal/web/server.go` — HTTP sunucu setup (i18n middleware dahil)
- `internal/web/middleware.go` — Auth, CSRF, rate limit, LAN-only, i18n
- `internal/web/auth.go` — Login/logout, bcrypt, session cookie
- `internal/tmpl/render.go` — Template rendering helper (i18n FuncMap entegrasyonu)
- `internal/tmpl/funcs.go` — Template fonksiyonları (`t`, `tp`, formatBytes, humanTime)
- `web/embed.go` — go:embed (templates + static + locales)
- `web/templates/layouts/base.html` — Ana layout (sidebar + content + lang-switch)
- `web/templates/layouts/auth.html` — Login layout
- `web/templates/pages/login.html`
- `web/templates/pages/dashboard.html` (placeholder)
- `web/templates/partials/sidebar.html` — Navigasyon (tüm etiketler `{{ t }}` ile)
- `web/templates/partials/toast.html`
- `web/static/css/reset.css`
- `web/static/css/variables.css`
- `web/static/css/layout.css`
- `web/static/css/components.css`
- `web/static/js/htmx.min.js`
- `web/static/js/app.js`

Adımlar:
1. `net/http.ServeMux` ile routing (Go 1.22+ pattern: `GET /login`, `POST /login`, `POST /settings/lang`)
2. `html/template` ile layout inheritance: `base.html` → `{{block "content" .}}`
3. **Template FuncMap'e i18n fonksiyonları ekle:**
   - `t`: `func(lang, key string) string` → çeviri döndür
   - `tp`: `func(lang, key string, params ...string) string` → parametreli çeviri
4. **Her handler'da `.Lang` context'e ekle:** `data.Lang = i18n.LangFromContext(r.Context())`
5. **Dil değiştirme handler:** `POST /settings/lang` → `lang` cookie set → `HX-Refresh: true`
6. **`<html lang="{{ .Lang }}">` attribute'u** base layout'ta dinamik
7. `go:embed` ile tüm static + template + locale dosyalarını binary'ye göm
8. Session: `gorilla/sessions` ile cookie-based (encrypted, httpOnly, secure, sameSite)
9. bcrypt ile password verify
10. Rate limiting: token bucket (stdlib `time.Ticker` + `sync.Map`)
11. CSRF: double-submit cookie (custom header `X-CSRF-Token`)
12. LAN-only: middleware'de source IP kontrolü
13. HTMX base layout: sidebar navigasyon (`{{ t .Lang "nav.dashboard" }}` vb.), content area, toast, lang-switch
14. Dark/light tema: CSS custom properties + JS toggle
15. **Tüm template'lerde sabit metin yok** — her label, buton, başlık `{{ t }}` fonksiyonu ile

Manuel doğrulama:
- `curl -k https://10.0.0.1:8443/login` → login sayfası dönüyor mu
- Yanlış şifre → login sayfasında hata mesajı (HTMX swap), dile uygun mesaj
- Doğru şifre → dashboard'a redirect
- WAN IP'den erişim → 403
- **Dil testi:** `Accept-Language: en` ile istek → İngilizce UI
- **Dil değiştirme:** TR/EN butonlarına tıkla → sayfa seçilen dilde yenileniyor mu
- **Sidebar:** tüm navigasyon etiketleri aktif dile göre mi

### Phase 3: PPPoE WAN Bağlantısı (3 gün)
**Hedef:** PPPoE ile internete bağlanma, auto-reconnect, bağlantı durum izleme.

Oluşturulacak dosyalar:
- `internal/services/pppoe.go`
- `configs/sysconf/pppoe-peer.tmpl`
- `configs/sysconf/pppoe-options.tmpl`
- `internal/web/handlers/pppoe.go`
- `internal/web/handlers/network.go`
- `web/templates/pages/network.html`
- `web/templates/partials/wan-status.html`

Adımlar:
1. `text/template` ile `/etc/ppp/peers/wan` ve options dosyası render
2. PPPoE service: Connect (`pppd call wan`), Disconnect (`kill pppd`), Status
3. Credentials `.credentials.enc`'den AES-256-GCM ile çözme
4. Auto-reconnect: pppd `persist` + `holdoff` seçenekleri
5. Agent operations: `pppoe.connect`, `pppoe.disconnect`, `pppoe.status`
6. Network handler: interface listesi, WAN IP, gateway, uptime
7. HTMX: bağlan/kes butonları → partial swap ile durum güncelleme
8. **i18n:** Tüm template metinleri `{{ t .Lang "pppoe.*" }}` ile — buton etiketleri, durum mesajları, onay diyalogları

Manuel doğrulama:
- `ppp0` interface ayağa kalkıyor mu
- İnternet erişimi: `ping 8.8.8.8`
- Auto-reconnect: pppd kill sonrası tekrar bağlanıyor mu
- Web UI'dan durum görünüyor + bağlan/kes çalışıyor mu
- TR/EN dillerinde tüm PPPoE metinleri doğru mu

### Phase 4: nftables Firewall + NAT (4 gün)
**Hedef:** Zone-based firewall, NAT masquerade, MSS clamping, port forwarding, watchdog rollback.

Oluşturulacak dosyalar:
- `internal/services/firewall.go`
- `internal/agent/watchdog.go`
- `configs/sysconf/nftables.conf.tmpl`
- `configs/defaults/firewall.yaml`
- `internal/web/handlers/firewall.go`
- `web/templates/pages/firewall.html`
- `web/templates/partials/fw_rules.html`
- `web/templates/partials/confirm.html`

Adımlar:
1. nftables Go `text/template` şablonu:
   - `table inet filter` — input/forward/output chains
   - `table ip nat` — prerouting (DNAT) + postrouting (masquerade)
   - MSS clamping: `tcp flags syn tcp option maxseg size set rt mtu`
   - Connection tracking: `ct state established,related accept`
   - WAN input: default drop, established + ICMP
   - LAN→WAN forward: accept + masquerade
   - Rate limiting: brute force koruması
2. AtomicChange: snapshot → validate (`nft -c -f`) → apply → watchdog
3. Watchdog: 30s goroutine timer, onay gelmezse rollback
4. Port forwarding: DNAT + forward kuralı CRUD
5. sysctl: `ip_forward=1`, ipv6 forwarding kapalı
6. HTMX: kural ekleme formu, silme, watchdog onay banner'ı
7. **i18n:** Tüm template metinleri `{{ t .Lang "firewall.*" }}` ile — kural tipleri, watchdog uyarısı, onay butonu

Manuel doğrulama:
- NAT çalışıyor mu (LAN → internet)
- WAN → LAN engelli mi
- Port forwarding çalışıyor mu
- Watchdog: onaylanmayan değişiklik 30s sonra rollback oluyor mu
- `nft list ruleset` beklenen kuralları gösteriyor mu
- TR/EN dillerinde firewall metinleri doğru mu

### Phase 5: Unbound DNS + dnsmasq DHCP (3 gün)
**Hedef:** Recursive DNS resolver + reklam engelleme (Unbound), DHCP sunucu (dnsmasq), config dosyası yönetimi.

Oluşturulacak dosyalar:
- `internal/services/dns.go` — Unbound config render, blocklist indirme, `unbound-control` wrapper
- `internal/services/dhcp.go` — dnsmasq config render, lease dosyası parse, SIGHUP reload
- `configs/sysconf/unbound.conf.tmpl` — Unbound config şablonu
- `configs/sysconf/dnsmasq.conf.tmpl` — dnsmasq DHCP-only config şablonu
- `internal/web/handlers/dns.go`
- `internal/web/handlers/dhcp.go`
- `web/templates/pages/dns.html`
- `web/templates/pages/dhcp.html`
- `web/templates/partials/dns-stats.html`
- `web/templates/partials/dns-blocklist.html`
- `web/templates/partials/lease_table.html`

Adımlar:
1. **Unbound config template:**
   - `server:` �� interface, access-control, cache-size, verbosity
   - Recursive mode: `root-hints` dosyası ile
   - Blocklist: `include: /etc/unbound/blocklist.conf` (her satır: `local-zone: "domain" always_refuse`)
   - Opsiyonel DNS-over-TLS upstream: `forward-zone:` → `forward-tls-upstream: yes`
2. **Blocklist yönetimi:**
   - StevenBlack/hosts formatını indir (`net/http`)
   - Parse: `0.0.0.0 domain` → `local-zone: "domain" always_refuse`
   - Atomic write → `unbound-control reload`
   - Zamanlanmış güncelleme (goroutine ticker)
3. **dnsmasq config template:**
   - `port=0` (DNS kapalı, sadece DHCP)
   - `dhcp-range=10.0.0.100,10.0.0.250,12h`
   - `dhcp-option=option:router,10.0.0.1`
   - `dhcp-option=option:dns-server,10.0.0.1` (Unbound'a yönlendir)
   - Statik lease'ler: `dhcp-host=aa:bb:cc:dd:ee:ff,10.0.0.10,desktop`
4. **Lease parse:** `/var/lib/misc/dnsmasq.leases` dosyasını oku → `{expiry, mac, ip, hostname}`
5. **DNS istatistikleri:** `unbound-control stats_noreset` → cache hits, misses, query count
6. **Cihaz listesi:** lease'lerden MAC+IP+hostname (VPN modülü kullanacak)
7. **Config değişikliği akışı:** Go template render → atomic write → agent `SIGHUP` gönder
8. **Agent operations:** `dns.reload` (unbound-control reload), `dhcp.reload` (SIGHUP dnsmasq)
9. HTMX: lease tablosu, DNS istatistikleri, blocklist durumu
10. **i18n:** Tüm template metinleri `{{ t .Lang "dns.*" }}` ve `{{ t .Lang "dhcp.*" }}` ile

Manuel doğrulama:
- `dig @10.0.0.1 google.com` → Unbound recursive çözümleme çalışıyor mu
- `dig @10.0.0.1 ads.example.com` → blocklist engelleme çalışıyor mu (REFUSED)
- DHCP: yeni cihaz IP alıyor mu, lease tablosunda görünüyor mu
- Statik lease ekle/sil çalışıyor mu
- `unbound-control stats_noreset` → istatistikler web UI'da doğru mu
- TR/EN dillerinde DNS ve DHCP sayfası metinleri doğru mu

### Phase 6: Dashboard + SSE Real-Time (3 gün)
**Hedef:** Ana dashboard, SSE ile real-time metrikler, Canvas grafikleri.

Oluşturulacak dosyalar:
- `internal/services/monitor.go`
- `internal/web/sse.go` — SSE broker
- `internal/web/handlers/dashboard.go`
- `web/templates/pages/dashboard.html` (tam)
- `web/templates/partials/stats_card.html`
- `web/templates/partials/bandwidth.html`
- `web/static/js/chart.js`
- `web/static/css/pages.css`

Adımlar:
1. Monitor service: goroutine, 1s interval — CPU, RAM, temp, throughput
   - `/proc/stat` (CPU), `/proc/meminfo` (RAM), `/sys/class/thermal` (temp)
   - `/proc/net/dev` (interface byte counters → throughput hesaplama)
2. SSE broker: channel-based pub/sub, goroutine per client
3. SSE endpoint: `GET /events/stats` → `text/event-stream`
4. Dashboard: stat kartları (uptime, WAN IP, CPU, RAM, throughput)
5. Canvas grafik: bandwidth history (son 60 veri noktası, 1s interval)
6. Responsive layout: CSS Grid, mobile-first
7. Settings sayfası: hostname, timezone, password değiştir
8. **i18n:** Dashboard stat etiketleri, birim formatları `{{ t .Lang "dashboard.*" }}` ile

Manuel doğrulama:
- Dashboard'da real-time metrikler güncelleniyor mu (SSE)
- Bandwidth grafiği canlı çiziliyor mu
- Mobil cihazdan responsive görünüyor mu
- TR/EN dillerinde dashboard metinleri doğru mu

### Phase 7: SQM/QoS — Bufferbloat Çözümü (3 gün)
**Hedef:** CAKE qdisc, ingress shaping, BBR/CUBIC, per-device limitleri.

Oluşturulacak dosyalar:
- `internal/services/qos.go`
- `internal/web/handlers/qos.go`
- `configs/defaults/qos.yaml`
- `web/templates/pages/qos.html`
- `web/templates/partials/qos-status.html`

Adımlar:
1. CAKE qdisc:
   - Egress: `tc qdisc add dev ppp0 root cake bandwidth {upload}kbit`
   - Ingress: IFB device → `tc qdisc add dev ifb0 root cake bandwidth {download}kbit wash ingress`
2. Congestion control: `sysctl net.ipv4.tcp_congestion_control={bbr|cubic}`
3. BBR prerequisite: `sysctl net.core.default_qdisc=fq`
4. Profiller: cake (varsayılan), fq_codel, none
5. Agent ops: `qos.apply`, `qos.clear`
6. HTMX: profil seçimi (radio), bandwidth input, apply butonu
7. **i18n:** QoS profil açıklamaları, etiketler, birimler `{{ t .Lang "qos.*" }}` ile

Manuel doğrulama:
- `tc -s qdisc show dev ppp0` → CAKE aktif mi
- Bufferbloat testi (flent rrul veya waveform.com/tools/bufferbloat)
- BBR/CUBIC geçişi çalışıyor mu
- TR/EN dillerinde QoS sayfası metinleri doğru mu

### Phase 8: WireGuard VPN + Policy Routing (5 gün)
**Hedef:** WireGuard tünelleri, per-device policy routing, drag-and-drop UI.

Oluşturulacak dosyalar:
- `internal/services/vpn.go`
- `configs/sysconf/wireguard.conf.tmpl`
- `configs/defaults/vpn.yaml`
- `internal/web/handlers/vpn.go`
- `web/templates/pages/vpn.html`
- `web/templates/partials/vpn_panel.html`
- `web/templates/partials/vpn_device.html`
- `web/static/js/htmx-sortable.js`

Adımlar:
1. WireGuard config template: key, endpoint, allowed IPs, DNS
2. Tünel CRUD: `wg-quick up/down wgN`
3. Keypair: `exec.Command("wg", "genkey")` + `wg pubkey`
4. Policy routing:
   - `ip route add default dev wgN table {table_id}`
   - nftables: `meta mark set {fwmark}` kaynak IP/MAC'e göre
   - `ip rule add fwmark {mark} lookup {table_id}`
   - `ct mark` ile reply paket fwmark korunması
5. nftables template güncelleme: VPN fwmark chain
6. Kill switch: VPN down → ilgili cihaz trafiği engelle
7. HTMX drag-and-drop:
   - HTML5 Drag and Drop API + HTMX `hx-trigger="drop"`
   - Sol panel: cihaz havuzu, sağ panel: tünel drop zone'ları
   - Drop → `PUT /vpn/assign` → partial swap
8. Startup restore: `vpn.yaml`'dan tünel + route'ları kur
9. **i18n:** Tünel isimleri hariç tüm UI metinleri `{{ t .Lang "vpn.*" }}` ile (drag-drop ipuçları, butonlar, durum etiketleri)

Manuel doğrulama:
- `wg show` → tünel aktif mi, handshake var mı
- Atanmış cihaz VPN'den çıkıyor mu (whatismyip)
- Atanmamış cihaz normal PPPoE'den çıkıyor mu
- Drag-and-drop anlık çalışıyor mu
- Kill switch: tünel down → cihaz internetsiz mi

### Phase 9: Samba NAS + M3U Parser (3 gün)
**Hedef:** Samba paylaşımları, M3U indirme/parse, Kodi-uyumlu medya yapısı.

Oluşturulacak dosyalar:
- `internal/services/nas.go`
- `configs/sysconf/smb.conf.tmpl`
- `configs/defaults/nas.yaml`
- `internal/web/handlers/nas.go`
- `web/templates/pages/nas.html`
- `web/templates/partials/share_list.html`
- `web/templates/partials/m3u-status.html`

Adımlar:
1. Samba config template: global + per-share
2. Paylaşım CRUD: oluştur/güncelle/sil → `smb.conf` regenerate → `smbcontrol reload-config`
3. M3U parser:
   - `net/http` ile M3U/M3U8 indir
   - `#EXTINF` parse: grup, başlık, URL
   - İçerikleri gruplara göre klasörlere indir (goroutine pool)
   - Kodi `.strm` dosyaları oluştur
4. Zamanlanmış sync: `time.Ticker` goroutine
5. HTMX: paylaşım listesi, M3U sync butonu, durum göstergesi
6. **i18n:** Paylaşım form etiketleri, M3U durum mesajları `{{ t .Lang "nas.*" }}` ile

Manuel doğrulama:
- Samba erişimi: Windows/macOS/Linux'tan bağlanabiliyor mu
- M3U parse: `.strm` dosyaları doğru klasör yapısında mı
- Kodi'den medya oynatılabiliyor mu
- TR/EN dillerinde NAS sayfası metinleri doğru mu

### Phase 10: Storage + Backup + Hardening (3 gün)
**Hedef:** RAID izleme, disk sağlığı, config backup, güvenlik sertleştirme.

Oluşturulacak dosyalar:
- `internal/services/storage.go`
- `internal/services/backup.go`
- `internal/web/handlers/storage.go`
- `internal/web/handlers/system.go`
- `web/templates/pages/storage.html`
- `web/templates/pages/settings.html`
- `web/templates/partials/raid_status.html`
- `deploy/factory-reset.sh`
- `deploy/backup.sh`

Adımlar:
1. RAID: `mdadm --detail` parse, degraded alarm
2. SMART: `smartctl -a` → sağlık skoru, sıcaklık, hata
3. Config backup: `tar.gz` export/import (config/ + unbound + dnsmasq config)
4. Factory reset: varsayılan config restore
5. Güvenlik sertleştirme:
   - systemd: ProtectSystem=strict, PrivateTmp, NoNewPrivileges
   - sysctl: rp_filter, tcp_syncookies, icmp_ignore_bogus
   - SSH: key-only, LAN-only
   - CSP header, X-Frame-Options, X-Content-Type-Options
6. HDD spin-up stagger: `hdparm -S`
7. **i18n:** Storage, settings, backup sayfaları `{{ t .Lang "storage.*" }}` ve `{{ t .Lang "settings.*" }}` ile
8. **i18n doğrulama:** Tüm locale JSON dosyalarında eksik anahtar testi (build time check)

Manuel doğrulama:
- RAID durumu doğru gösteriliyor mu
- Config export → factory reset → import → çalışıyor mu
- Güvenlik header'ları mevcut mu (`curl -I`)
- TR/EN dillerinde storage ve settings sayfaları doğru mu
- **i18n bütünlük:** `tr.json` ve `en.json` aynı anahtarlara sahip mi (eksik anahtar yok)

---

## Veri Akış Diyagramları

### PPPoE Bağlantı Akışı
```
Tarayıcı: <button hx-post="/pppoe/connect">
  → Go Handler: pppoeConnect(w, r)
    → authMiddleware: session cookie doğrula
    → pppoeSvc.Connect(ctx)
      → config'den credentials çöz (AES-256-GCM)
      → text/template: /etc/ppp/peers/wan render
      → agentClient.Call("pppoe.connect", params)
        → Agent goroutine: exec.Command("pppd", "call", "wan")
        → ppp0 interface ayağa kalkar
        → return {status: "connected", ip: "..."}
      → firewallSvc.Apply() tetikle → NAT + MSS clamping aktif
    → tmpl.Render(w, "partials/wan-status.html", data)
  → HTMX: #wan-status outerHTML swap
```

### VPN Drag-and-Drop Akışı
```
Tarayıcı: drag device-card → drop vpn-tunnel-zone
  → hx-put="/vpn/assign" + hx-vals={mac, tunnel}
  → Go Handler: vpnAssign(w, r)
    → vpnSvc.AssignDevice(mac, tunnelName)
      → vpn.yaml atomic write
      → nftables fwmark kuralı oluştur
      → agentClient.Call("firewall.apply", nftRules)
      → agentClient.Call("routing.addRule", {fwmark, table})
    → tmpl.Render(w, "partials/vpn_panel.html", data)
  → HTMX: #vpn-panel outerHTML swap
  → SSE: "vpn-changed" event → diğer client'lara bildir
```

### Atomic Firewall Change Akışı
```
firewallSvc.Apply(rules)
  → atomic.Snapshot(): exec("nft list ruleset") > backup
  → atomic.Validate(): exec("nft -c -f", newRules)  // dry-run
  → atomic.Apply(): agentClient.Call("firewall.apply")
  → watchdog goroutine başlat (30s)
    → <-timer.C: rollback exec("nft -f", backup)
    → <-confirmCh: timer.Stop(), backup sil
  → Handler: render "partials/confirm.html" (countdown + onay butonu)
  → Tarayıcı: <button hx-post="/firewall/confirm">
    → confirmCh <- struct{}{}
    → render "partials/toast.html" (başarılı)
```

---

## Risks and Trade-offs

| Risk                                    | Mitigation                                                              |
|-----------------------------------------|-------------------------------------------------------------------------|
| PMTU black-holing (PPPoE MTU 1492)      | Phase 4'te MSS clamping zorunlu                                        |
| NIC isimlendirme değişimi (reboot)      | udev rules by MAC address (`setup-interfaces.sh`)                      |
| VPN policy route'lar reboot'ta kaybolur | Agent startup'ta `vpn.yaml`'dan restore                                |
| Firewall kuralı hatalı → ağ kilitlenir | AtomicChange + 30s watchdog rollback                                   |
| PicoPSU 180W, 6 disk ile surge riski   | HDD spin-up stagger (`hdparm -S`)                                      |
| Web UI XSS                              | `html/template` auto-escaping + CSP header + agent op whitelist        |
| PPPoE credential sızıntısı             | AES-256-GCM encryption at rest, memory-only decrypt                    |
| Unbound/dnsmasq crash → DNS/DHCP çalışmaz | systemd restart policy + Go health check + degraded mode UI uyarısı  |
| Single point of failure (tek cihaz)    | Config backup + factory reset + RAID-1 depolama                        |
| Go binary update sırasında downtime    | systemd: `ExecStartPre` ile binary swap, graceful shutdown             |
| HTMX: full page refresh gerekebilir   | `hx-boost` ile link'leri HTMX'e çevir, minimal JS fallback            |

## Tahmini Toplam Süre

| Phase | Konu                          | Gün | Kümülatif |
|-------|-------------------------------|-----|-----------|
| 1     | İskelet + Agent IPC           | 3   | 3         |
| 2     | Web + Auth + HTMX Layout      | 3   | 6         |
| 3     | PPPoE WAN                     | 3   | 9         |
| 4     | nftables Firewall + NAT       | 4   | 13        |
| 5     | Unbound DNS + dnsmasq DHCP    | 3   | 16        |
| 6     | Dashboard + SSE               | 3   | 19        |
| 7     | SQM/QoS                       | 3   | 22        |
| 8     | WireGuard VPN + Policy Routing| 5   | 27        |
| 9     | Samba NAS + M3U               | 3   | 30        |
| 10    | Storage + Backup + Hardening  | 3   | 33        |

**Toplam: ~33 geliştirme günü** (tek geliştirici, her gün 4-6 saat efektif çalışma varsayımı)
