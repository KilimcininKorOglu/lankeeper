# LANKeeper Software — Implementation Plan (Go + HTMX)

## Context

Turkcell Superonline'ın ISP modemleri bufferbloat sorununa neden oluyor ve 1 Gbps bağlantıda SQM/QoS desteği sunmuyor. Mevcut ZTE modem yerine Intel i5 3470 tabanlı özel donanım üzerine sıfırdan router yazılımı geliştirilecek. Hedef: PPPoE WAN bağlantısı, nftables firewall, WireGuard VPN, Samba NAS ve web dashboard'u tek bir Go binary'sinde birleştirmek.

## Kurallar

1. **Her değişiklikte commit atılır.** Fonksiyonel bir birim tamamlandığında hemen commit.
2. **Asla yama yapılmaz.** Sorunun kök nedeni bulunur ve oradan çözülür.
3. **Çoklu dil desteği (i18n) ilk günden zorunludur.** Tüm UI metinleri locale JSON dosyalarından gelir, template'lere sabit metin yazılmaz.

## Neden Go + HTMX?

| Kriter           | Python + FastAPI + Vanilla JS        | Go + HTMX                                   |
|------------------|--------------------------------------|---------------------------------------------|
| Deployment       | venv + pip + uvicorn + systemd       | Tek statik binary, `scp` ile deploy         |
| Bellek           | ~80-120 MB (Python runtime + deps)   | ~10-20 MB (compiled binary)                 |
| Startup          | 2-5 saniye (import + uvicorn)        | <100ms                                      |
| Concurrency      | asyncio (single-threaded event loop) | goroutine (lightweight threads, multi-core) |
| Frontend         | Client-side SPA, JS state yönetimi   | Server-side HTML, HTMX partial swap         |
| Type safety      | Runtime (Pydantic)                   | Compile-time (structs)                      |
| Bağımlılık       | ~12 pip paketi                       | stdlib + 4-5 Go modülü                      |
| Router için uyum | Orta (GC pauses, memory overhead)    | Yüksek (düşük latency, düşük bellek)        |

## Current State

Bu bölüm proje ilerledikçe güncellenir. Aşağıdaki plan metninin geri kalanı, aksi belirtilmedikçe **tasarım niyetini** anlatır; mimari sapmalar "Plan ile Gerçek Arasındaki Sapmalar" bölümünde, eksik kalan özellikler "Sonraki adaylar" bölümündedir.

**Son sürüm:** v0.5.0 (`git describe` → `v0.5.0-100-g1bee872`), 419 commit.

| Ölçüt | Değer |
|---|---|
| Go dosyası | 231 (138'i `_test.go`) |
| Servis | 26 domain, 38 non-test dosya (`internal/services/`) |
| Handler | 23 dosya, 133 HTTP route (`internal/web/handlers/`) |
| Sistem config şablonu | 17 (`configs/sysconf/`) |
| Locale anahtarı | 679 (tr.json ve en.json senkron) |
| Agent komut whitelist | 47 komut |
| Harici Go bağımlılığı | 6 direct modül |
| Hedef mimari | linux/amd64 + linux/arm64 |

**Tamamlanan:** 11 implementation phase'in tamamı, ayrıca v0.2.0-v0.5.0 roadmap başlıkları (IPv6 PD UI, 6in4 tunneling, Prometheus metrics, DoH upstream, backup scheduling, per-client bandwidth, WireGuard S2S wizard, OTA update, preseed ISO builder).

**Donanım:** 2x Gigabit NIC, RAID-1 depolama, Debian 12 Bookworm (minimal), Intel i5 3470.

## What We're NOT Doing

- Wi-Fi yönetimi (kullanıcı ayrı AP'ler kullanıyor)
- Harici DNS/DHCP web UI (Pi-hole, AdGuard Home) — Unbound + dnsmasq doğrudan Go'dan yönetilecek
- Veritabanı (tüm config YAML dosyalarında)
- JavaScript framework (React/Vue/Svelte yok — HTMX + server-side rendering)
- Çoklu ISP / load balancing (tek PPPoE ana bağlantı + USB tethering yedek)
- Konteyner/Docker desteği (ÜRÜNDE. Docker yalnızca ISO derlemek için build makinesinde kullanılır)
- ORM veya SQL — dosya tabanlı config

---

## Plan ile Gerçek Arasındaki Sapmalar

Bu plan tasarım aşamasında yazıldı. Uygulama bazı kararları değiştirdi. Aşağıdakiler mimari seviyedeki sapmalardır; özellik seviyesindeki eksikler "Sonraki adaylar" bölümündedir.

| Konu | Plan | Gerçek |
|---|---|---|
| Partial endpoint şeması | Her partial için ayrı `GET /partials/*` route'u | Partial'lar sayfa handler'ı içinde render edilir; `/partials/*` route'u YOK |
| Route sayısı ve yolları | 127 route, `/pppoe/*` gibi kısa önekler | 133 route, `/network/pppoe/*` gibi alan altına yuvalanmış yollar |
| Kaynak düzenleme | Çoğu liste için POST + PUT + DELETE | Ekle + sil; PUT yok, toggle yalnızca firewall kuralları ve açık portlarda |
| Harici Go bağımlılığı | 3 modül | 6 direct modül (DoT/DoH probe, SFTP hedefi, fsnotify eklendi) |
| Sayfa dağılımı | Interface, VLAN, PPPoE, health ayrı sayfalar | Hepsi tek `network.html` sayfasında; ayrıca planda olmayan `ipv6.html`, `backup.html`, `vpn-s2s.html` eklendi |
| Config şeması | 17 üst düzey anahtar | 19 anahtar (`routing`, `backup` eklendi; `ipv6.tunnel` alt bloğu 6in4 için, `system.tls.acme.directoryUrl` ve `system.tls.mkcert.sans` TLS modları için eklendi) |
| Deploy | `scp` + `systemctl restart` | `make install`, offline preseed ISO, ve web UI'dan OTA update |
| Release otomasyonu | `.goreleaser.yaml` (opsiyonel) | goreleaser YOK; arşiv ve checksum üretimi Makefile hedeflerinde |
| Hedef mimari | linux/amd64 | linux/amd64 + linux/arm64 |
| Per-client QoS istatistiği | CAKE class stats | CAKE per-host stats netlink-only ve pretty-print edilmiyor; ayrı `lankeeper_qos` nftables sayaç tablosu yazıldı |
| Test paketleri | Yalnızca `_test.go` dosyaları paket içinde | Ayrıca üretim kodu içermeyen `buildsys/` ve `deploy/iso/` test-only paketleri; build recipe, CI pin ve installer script özelliklerini denetler |
| DoH upstream | Unbound doğrudan DoH konuşur | Unbound hiçbir sürümde DoH upstream desteklemiyor; `dnscrypt-proxy` stub'ı zorunlu ara katman oldu |

---

## Mimari Kararlar

### 1. Tek Binary, İki Mod

Python'daki iki ayrı process (agent + web) yerine, Go'da **tek binary iki modda** çalışır:

```
lankeeper
├── lankeeper serve    → Web sunucu (unprivileged, capability: CAP_NET_BIND_SERVICE)
└── lankeeper agent    → Privileged agent (root, UDS listener)
```

```
┌─────────────────────────────┐     ┌──────────────────────────────┐
│  lankeeper serve          │     │  lankeeper agent           │
│  User: lankeeper           │────▶│  User: root                  │
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
- IPC: Unix domain socket (`/run/lankeeper/agent.sock`) + JSON-RPC 2.0
- Tek binary: `go build -o lankeeper ./cmd/lankeeper`

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

### UI Design System — X (Twitter) İlhamlı

Minimalist, content-first, dark-mode dominant. X (Twitter) tasarım dili temel alınarak router dashboard'una uyarlanmış.

**Renk paleti (CSS custom properties):**

```css
:root {
  /* Dark mode (varsayılan) */
  --bg-primary: #000000;          /* Ana arka plan */
  --bg-surface: #16181C;          /* Kartlar, paneller */
  --bg-elevated: #1D1F23;         /* Hover state, dropdown */
  --border-color: #2F3336;        /* Ayırıcılar, kart kenarları */

  --text-primary: #E7E9EA;        /* Ana metin */
  --text-secondary: #71767B;      /* İkincil metin, timestamp */
  --text-tertiary: #536471;       /* Placeholder, devre dışı */

  --accent-blue: #1D9BF0;         /* Link, aktif öğe, birincil buton */
  --accent-green: #00BA7C;        /* Bağlı, aktif, sağlıklı */
  --accent-red: #F4212E;          /* Hata, bağlantı kopuk, tehlike */
  --accent-yellow: #FFD400;       /* Uyarı, dikkat */
  --accent-pink: #F91880;         /* Vurgulama, özel durum */

  /* Focus ring */
  --focus-ring: 0 0 0 2px #1D9BF0;

  /* Spacing (4px base) */
  --space-xs: 4px;
  --space-sm: 8px;
  --space-md: 12px;
  --space-lg: 16px;
  --space-xl: 20px;
  --space-2xl: 24px;
  --space-3xl: 32px;
  --space-4xl: 48px;
}

/* Light mode */
[data-theme="light"] {
  --bg-primary: #FFFFFF;
  --bg-surface: #F7F9F9;
  --bg-elevated: #EFF3F4;
  --border-color: #EFF3F4;
  --text-primary: #0F1419;
  --text-secondary: #536471;
  --text-tertiary: #71767B;
}
```

**Tipografi:**
```css
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  font-size: 15px;
  line-height: 20px;
  color: var(--text-primary);
  background-color: var(--bg-primary);
}
```

| Öğe           | Boyut | Ağırlık | Satır Yüksekliği |
|---------------|-------|---------|------------------|
| Sayfa başlığı | 23px  | 700     | 28px             |
| Bölüm başlığı | 20px  | 700     | 24px             |
| Gövde metin   | 15px  | 400     | 20px             |
| Gövde kalın   | 15px  | 700     | 20px             |
| Alt metin     | 13px  | 400     | 16px             |
| Küçük         | 12px  | 400     | 16px             |

**Bileşen stilleri:**

```css
/* Birincil buton (mavi) */
.btn-primary {
  background-color: var(--accent-blue);
  color: #FFFFFF;
  font-size: 15px;
  font-weight: 700;
  padding: 0 var(--space-lg);
  height: 36px;
  border-radius: 9999px;
  border: none;
  transition: background-color 0.2s ease;
}

/* Kart */
.card {
  background-color: var(--bg-surface);
  border: 1px solid var(--border-color);
  border-radius: 16px;
  padding: var(--space-md) var(--space-lg);
}

/* Sidebar navigasyon */
.nav-item {
  padding: var(--space-md);
  border-radius: 9999px;
  color: var(--text-primary);
  font-size: 20px;
  font-weight: 400;
  transition: background-color 0.2s ease;
}
.nav-item:hover {
  background-color: var(--bg-elevated);
}
.nav-item.active {
  font-weight: 700;
}

/* Durum göstergesi */
.status-ok { color: var(--accent-green); }
.status-error { color: var(--accent-red); }
.status-warning { color: var(--accent-yellow); }

/* Divider */
.divider {
  border-bottom: 1px solid var(--border-color);
}

/* Dropdown/modal gölge */
.floating {
  background-color: var(--bg-primary);
  border-radius: 12px;
  box-shadow: 0 0 0 1px rgba(255, 255, 255, 0.15),
              0 0 15px rgba(255, 255, 255, 0.1);
}

/* Sticky header (blur) */
.header-blur {
  background-color: rgba(0, 0, 0, 0.65);
  backdrop-filter: blur(12px);
}
```

**Layout:**
- Sidebar (sol): sabit, 275px genişlik — navigasyon + logo
- İçerik (orta): akışkan, max-width 600px — ana sayfa içeriği
- Panel (sağ): sabit, 350px — durum kartları, ek bilgi (opsiyonel)
- Mobil: sidebar → bottom tab bar, panel gizlenir
- CSS Grid: `grid-template-columns: 275px minmax(0, 600px) 350px`

**Animasyon:**
- Geçişler: `transition: all 0.2s ease-out` (standart)
- Modal: `scale(0.95) → scale(1)` + `opacity: 0 → 1`
- Toast: aşağıdan yukarı kayma, 3s sonra otomatik kapanma
- Yükleme: skeleton shimmer (CSS animation)

**Tema geçişi:**
- `data-theme` attribute'u `<html>` tag'inde (`dark` | `light`)
- Varsayılan: dark mode
- Toggle: JS ile `data-theme` değiştir + `localStorage`'a kaydet + cookie'ye yaz (server-side render için)
- `prefers-color-scheme: light` medya sorgusu ile otomatik algılama (kullanıcı override edebilir)

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
    "nav.routing": "Routing",
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
    "auth.logout": "Logout",
    "tls.title": "TLS Certificate",
    "tls.mode": "Mode",
    "tls.selfSigned": "Self-Signed",
    "tls.mkcert": "mkcert (Local CA)",
    "tls.acme": "Let's Encrypt",
    "tls.expires": "Expires",
    "tls.regenerate": "Regenerate Certificate",
    "tls.downloadCa": "Download CA Certificate",
    "tls.issuer": "Issuer"
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

### 5. Policy-Based Routing (PBR) Motoru

Basit "cihaz → VPN" atamasının ötesinde, çok boyutlu politika kuralları:

**Match kriterleri:**
- Kaynak: cihaz (MAC/IP/hostname), subnet (CIDR)
- Hedef: IP, CIDR, domain (DNS-based)
- Port / protokol: TCP/UDP + port aralığı
- Zaman: schedule (cron-like, ör: "22:00-08:00", "weekdays")
- Kombinasyon: yukarıdakilerin hepsi AND ile birleştirilebilir

**Action'lar:**
- `wan` — direkt PPPoE çıkış
- `{tunnel_name}` — belirli WireGuard tünelinden çıkış
- `drop` — trafiği engelle

**Priority:** Düşük sayı = yüksek öncelik. Web UI'da sürükle-bırak ile sıralama.

**Akış:**
```
Paket gelir → nftables PBR chain
  → priority sırasıyla kural eşleştirme:
    1. match: src_device + dst_port + protocol + schedule
    2. eşleşen kural → fwmark ata
    3. eşleşmeyen → sonraki kural
    4. hiçbiri eşleşmez → default route (wan)
  → ip rule fwmark X lookup table Y
  → table Y: default via wgN veya ppp0
  → ct mark: reply paketlerde fwmark korunur
```

**Domain-based routing mekanizması:**
```
1. Politika kuralında "dstDomains: [netflix.com, *.nflxvideo.net]" tanımlı
2. Go service: domain listesini Unbound'a local-data olarak ekler
   → Unbound DNS yanıtını çözer
3. Go service: DNS yanıtından çözümlenen IP'leri yakalar
   (unbound-control dump_cache veya Unbound Python module)
4. Çözümlenen IP'ler → nftables named set'e eklenir:
   nft add element inet filter pbr_netflix { 1.2.3.4, 5.6.7.8 }
5. nftables kuralı: ip daddr @pbr_netflix meta mark set {fwmark}
6. TTL dolduğunda IP set'ten kaldırılır, yeni DNS sorgusu yeni IP ekler
```

**Config (routing.yaml):**
```yaml
defaultRoute: "wan"

policies:
  - name: "gaming-direct"
    enabled: true
    priority: 100
    match:
      srcDevices: ["xbox", "ps5"]
      dstPorts: [3074, 3478, 3479]
      protocol: "udp"
    action:
      route: "wan"

  - name: "streaming-nl"
    enabled: true
    priority: 200
    match:
      dstDomains: ["netflix.com", "*.nflxvideo.net", "disneyplus.com"]
    action:
      route: "nl-amsterdam"

  - name: "laptop-vpn"
    enabled: true
    priority: 300
    match:
      srcDevices: ["laptop"]
    action:
      route: "de-frankfurt"

  - name: "night-vpn"
    enabled: true
    priority: 500
    match:
      schedule: "22:00-08:00"
      srcDevices: ["*"]
    action:
      route: "nl-amsterdam"

  - name: "torrent-block"
    enabled: false
    priority: 600
    match:
      dstPorts: [6881-6889]
      protocol: "tcp"
    action:
      route: "drop"
```

**Web UI (HTMX):**
- Politika listesi: sürükle-bırak ile priority sıralama
- Politika ekleme/düzenleme: form → match kriterleri + action seçimi
- Cihaz seçimi: DHCP lease'lerden dropdown (hostname + MAC)
- Domain girişi: metin alanı, wildcard (*.domain.com) destekli
- Tünel seçimi: aktif WireGuard tünellerinden dropdown
- Schedule: görsel zaman aralığı seçici
- Enable/disable toggle: politikayı devre dışı bırak (silmeden)
- Canlı durum: hangi cihaz hangi politikaya eşleşiyor (SSE ile)

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

### 7. IPv6 Dual-Stack Yaklaşımı

Tam dual-stack: IPv4 ve IPv6 paralel çalışır, NAT66 **yapılmaz**.

```
ISP (PPPoE) ─── IPv4: NAT masquerade (10.10.10.0/24 → WAN IP)
             └── IPv6: DHCPv6-PD ile global prefix → LAN'a doğrudan dağıtım (NAT yok)

LAN cihazı:
  IPv4: 10.10.10.x (SNAT/masquerade ile internete çıkış)
  IPv6: 2001:db8:1::x (global unicast, doğrudan internete çıkış)
        fd00:abcd:1234::x (ULA, ISP prefix olmasa bile LAN içi IPv6)
```

**Temel kararlar:**
- `table inet filter` zaten dual-stack hazır (IPv4 + IPv6 tek tabloda)
- `table ip nat` yalnızca IPv4 — IPv6 için NAT66 **eklenmeyecek**
- ISP'den DHCPv6-PD ile /56 veya /64 prefix alınır → LAN'a SLAAC ile dağıtılır
- ULA (fd00::/8) prefix: ISP IPv6 sunmasa bile LAN cihazları arası IPv6 bağlantı
- ICMPv6 zorunlu: NDP (Neighbor Discovery), RA (Router Advertisement), MLD — engellenirse IPv6 tamamen çalışmaz
- `ipv6.enabled: auto` → IPv6CP negotiation başarılırsa otomatik etkinleşir
- Privacy extensions: RA'da önerilir (RFC 4941, temporary addresses)

### 8. TLS Sertifika Yönetimi

Web UI her zaman HTTPS üzerinden çalışır. Üç mod desteklenir:

```
┌─────────────────────────────────────────────────────────────────┐
│  Mode           │ Kullanım                │ Tarayıcı Güveni     │
├─────────────────┼─────────────────────────┼──────────────────────┤
│  self-signed    │ Varsayılan, sıfır       │ Uyarı verir,        │
│                 │ yapılandırma gerekli    │ exception eklenir    │
├─────────────────┼─────────────────────────┼──────────────────────┤
│  mkcert         │ LAN geliştirme/ev       │ CA yüklü cihazlarda │
│                 │ kullanımı               │ tam güven (yeşil)    │
├─────────────────┼─────────────────────────┼──────────────────────┤
│  acme           │ Public domain +         │ Tam güven            │
│                 │ DNS challenge           │ (Let's Encrypt CA)   │
└─────────────────┴─────────────────────────┴──────────────────────┘
```

**Self-signed (varsayılan):**
- İlk başlatmada Go `crypto/x509` + `crypto/ecdsa` (P-256) ile otomatik üretilir
- SAN: LAN IP + hostname + `*.local` — tarayıcı uyarısı verir ama çalışır
- 10 yıl geçerlilik, `/var/lib/lankeeper/tls/` altında saklanır
- Yenileme: web UI'dan tek tıkla yeni cert üret. `EnsureTLSCert` süresi dolmamış sertifikayı KORUR, bu yüzden ayrı bir `RegenerateSelfSigned` yolu var

**mkcert (LAN kullanımı):**
- `mkcert` komutu ile lokal CA oluşturulur ve sertifika imzalanır
- CA sertifikası LAN cihazlarına yüklenirse tarayıcı uyarısı olmaz
- Web UI: "CA Sertifikası İndir" butonu → cihazlara import
- Agent komutu: whitelist'e `mkcert` eklendi; `CAROOT` agent tarafındaki `commandEnv` tablosunda sabit
- `mkcert -install` ÇAĞRILMAZ: router istemci değil sunucu; CA zaten ilk imzada oluşur
- Sertifika `/var/lib/lankeeper/mkcert/` altına stage'lenir, okunur ve serving dizinine servis kullanıcısı tarafından yazılır (agent root çalıştığı için doğrudan yazım okunamayan bir key bırakırdı)
- System dependency: `mkcert` (Debian paketi; hem `deploy/install.sh` hem `deploy/iso/build-iso.sh` listelerinde)

**Let's Encrypt (ACME):**
- LAN-only router'da HTTP-01 challenge çalışmaz → DNS-01 challenge zorunlu
- Desteklenen DNS provider'lar: Cloudflare (API) ve manual (TXT record). Route53 kapsam dışı
- Go ACME client: `golang.org/x/crypto/acme` (RFC 8555 doğrudan; `autocert` HTTP-01/TLS-ALPN odaklı, `lego` yeni bir modül olurdu)
- Varsayılan CA ortamı **staging**: production haftada beş sertifika ile sınırlı, yanlış yapılandırma günlerce bekletir
- Tüm outbound istekler `safefetch.go` içindeki korumalı client üzerinden gider
- Public domain gerekli (ör: `router.example.com`)
- Otomatik yenileme: cert expire'a 30 gün kala goroutine ile renew
- DNS API token'ı `enc:v1:` AES-256-GCM ile `router.yaml` içinde şifreli saklanır ve forma geri yazılmaz

**Ortak:**
- Tüm modlarda HSTS header gönderilmez (LAN-only, IP erişimi bozulur)
- TLS 1.2+ zorunlu, TLS 1.0/1.1 kapalı
- Cipher suite: Go'nun varsayılan güvenli seti (ECDHE + AES-GCM)
- Sertifika değişikliği `systemctl restart lankeeper.target` ile uygulanır; restart HTTP cevabı yazıldıktan SONRA ayrı bir goroutine'den tetiklenir
- Mod değişimi sırası kesindir: önce sertifika üretilir ve doğrulanır, ANCAK ondan sonra mod yazılır. Ters sıra operatörü kendi router'ından kilitler

### 9. Deployment: İki Katmanlı Kurulum

İki farklı kurulum yöntemi, aynı sonuç:

**Katman 1 — `install.sh` (interaktif):**
- Mevcut Debian 12 minimal kurulumu üzerine çalışır
- Admin interaktif olarak sorulara cevap verir (şifre, interface, WAN tipi)
- Idempotent: tekrar çalıştırılabilir, mevcut config'i bozmaz
- Kullanım: `sudo ./deploy/install.sh`

**Katman 2 — Debian Preseed ISO (sıfır dokunuş):**
- USB'den boot → tam otomatik: disk bölümleme (RAID-1) + OS kurulumu + tüm paketler + Go binary
- İlk boot'ta web UI'da setup wizard (admin şifresi, PPPoE, interface seçimi)
- Kullanım: USB'ye yaz → boot → 15 dk sonra router hazır
- `make iso` ile oluşturulur (Makefile entegrasyonu)

Her iki yöntem de aynı `post-install.sh` / `install.sh` mantığını paylaşır — tek fark interaktif vs preseed ile cevap verme.

### 10. Config Yönetimi

```go
// YAML config → Go struct (compile-time type safety)
type Config struct {
    System     SystemConfig     `yaml:"system"`
    Interfaces InterfaceConfig  `yaml:"interfaces"`
    HealthCheck HealthCheckConfig `yaml:"healthCheck"`
    PPPoE      PPPoEConfig      `yaml:"pppoe"`
    Firewall   FirewallConfig   `yaml:"firewall"`
    QoS        QoSConfig        `yaml:"qos"`
    DNS        DNSConfig        `yaml:"dns"`
    DHCP       DHCPConfig       `yaml:"dhcp"`
    IPv6       IPv6Config       `yaml:"ipv6"`
    VPN        VPNConfig        `yaml:"vpn"`
    OpenVPN    OpenVPNConfig    `yaml:"openvpn"`
    Routing    RoutingConfig    `yaml:"routing"`
    NAS        NASConfig        `yaml:"nas"`
    Syslog     SyslogConfig     `yaml:"syslog"`
    NTP        NTPConfig        `yaml:"ntp"`
    Storage    StorageConfig    `yaml:"storage"`
}
```

- Atomic write: tmp → fsync → rename
- Credentials: AES-256-GCM ile şifreleme (Go `crypto/aes` + `crypto/cipher`)
- Validation: struct tag'ler + custom validator fonksiyonlar

---

## Dizin Yapısı

```
lankeeper/
├── cmd/
│   └── lankeeper/
│       ├── main.go               # CLI dispatch + version/commit/date ldflags hedefi
│       ├── serve.go              # serve subcommand: agent client wiring + web.Serve
│       ├── agent.go              # agent subcommand: root UDS sunucusu
│       ├── gen-cert.go           # gen-cert subcommand: self-signed sertifika üretimi
│       └── render-configs.go     # render-configs subcommand: kurulum anında tüm sysconf render
├── internal/
│   ├── agent/
│   │   ├── server.go             # Root agent — UDS listener, op dispatcher, 16 eşzamanlı bağlantı sınırı
│   │   ├── client.go             # Web'den agent'a IPC istemcisi
│   │   ├── operations.go         # Komut whitelist (46) + write/read path kuralları + trustedBinDirs çözümlemesi
│   │   ├── watchdog.go           # Rollback watchdog timer (goroutine)
│   │   ├── peercred_linux.go     # SO_PEERCRED ile peer UID doğrulaması
│   │   └── peercred_other.go     # Linux dışı build için no-op
│   ├── config/
│   │   ├── config.go             # YAML load/save, struct tanımları, SaveToFile
│   │   ├── crypto.go             # AES-256-GCM encrypt/decrypt
│   │   ├── secrets.go            # enc:v1: prefix'li alan şifreleme + /var/lib key dosyası
│   │   ├── tls.go                # TLS sertifika yönetimi (self-signed, ACME, mkcert)
│   │   ├── defaults.go           # Varsayılan config değerleri
│   │   └── validate.go           # Config doğrulama
│   ├── web/
│   │   ├── server.go             # HTTP sunucu setup, tüm servis/handler wiring, setupRoutes (133 route)
│   │   ├── middleware.go         # CSRF, rate limit, LAN-only, security headers, request log
│   │   ├── auth.go               # Login/logout, session/cookie, bcrypt
│   │   ├── login_guard.go        # Login lockout + brute-force sayacı
│   │   ├── errors.go             # Lokalize HTTP hata cevapları
│   │   ├── sse.go                # SSE broker (broker başına 32 stream sınırı + 30s keep-alive)
│   │   └── handlers/
│   │       ├── dashboard.go      # GET / → dashboard sayfası
│   │       ├── network.go        # Interface + VLAN + PPPoE + USB + health birleşik sayfası
│   │       ├── vlan.go           # VLAN CRUD
│   │       ├── pppoe.go          # WAN bağlantı yönetimi + credential sniff
│   │       ├── healthcheck.go    # Health check reset (HandleStatus tanımlı ama route'a bağlı DEĞİL)
│   │       ├── firewall.go       # nftables kuralları + watchdog confirm/rollback
│   │       ├── dns.go            # Unbound DNS + blocklist + DoT/DoH şifreleme modu
│   │       ├── dhcp.go           # dnsmasq DHCP lease yönetimi
│   │       ├── ipv6.go           # DHCPv6-PD + 6in4 tunnel + subnet map
│   │       ├── qos.go            # SQM/QoS profilleri
│   │       ├── vpn.go            # WireGuard client/server + S2S wizard
│   │       ├── openvpn.go        # OpenVPN server/client + PKI
│   │       ├── routing.go        # Policy-based routing (CRUD + sıralama)
│   │       ├── nas.go            # Samba paylaşımları + M3U
│   │       ├── storage.go        # RAID durumu, disk sağlığı
│   │       ├── syslog.go         # Syslog sunucu/client yapılandırma
│   │       ├── ntp.go            # NTP sunucu/client yapılandırma + durum
│   │       ├── backup.go         # Zamanlanmış yedekleme hedefleri + history
│   │       ├── metrics.go        # Prometheus /metrics exposition
│   │       ├── system.go         # Ayarlar, yedekleme, OTA update, reboot, factory reset
│   │       ├── download.go       # Gizli dosya indirmelerinde Cache-Control: no-store
│   │       ├── respond.go        # Paylaşılan HTMX cevap yardımcıları
│   │       └── errors.go         # Handler seviyesinde lokalize hata cevapları
│   ├── services/
│   │   ├── network.go            # Interface + VLAN yönetimi
│   │   ├── pppoe.go              # pppd yönetimi + credential sniff
│   │   ├── usbtethering.go       # Android USB tethering failover (SERVİS VAR, ROUTE YOK)
│   │   ├── healthcheck.go        # Interface internet checker + otomatik recovery
│   │   ├── firewall.go           # nftables ruleset oluşturma + AtomicChange + 30s watchdog
│   │   ├── dns.go                # Unbound config + blocklist + unbound-control
│   │   ├── doh.go                # dnscrypt-proxy stub orchestration (DoH upstream)
│   │   ├── doh_resolvers.go      # 10 hazır DoH sağlayıcı + sdns:// stamp parser
│   │   ├── dhcp.go               # dnsmasq config + lease parse + DNS mirror DI
│   │   ├── ipv6.go               # dhcp6c PD + RA drop-in + fsnotify lease watcher
│   │   ├── sixinfour.go          # HE.net 6in4 tunnel + DDNS update
│   │   ├── qos.go                # tc + CAKE qdisc + IFB ingress shaping
│   │   ├── qos_clients.go        # Per-MAC nftables counter tablosu (lankeeper_qos)
│   │   ├── vpn.go                # WireGuard tunnel + peer yönetimi
│   │   ├── vpn_s2s.go            # Site-to-site invite/ack token state machine
│   │   ├── openvpn.go            # OpenVPN server + client + easy-rsa PKI
│   │   ├── routing.go            # Policy-based routing motoru (PBR)
│   │   ├── nas.go                # Samba config + M3U parser
│   │   ├── storage.go            # mdadm + smartctl + fstab
│   │   ├── monitor.go            # Sistem istatistikleri toplayıcı (goroutine)
│   │   ├── syslog.go             # rsyslog config yönetimi (sunucu + client)
│   │   ├── ntp.go                # chrony config yönetimi (sunucu + client)
│   │   ├── system.go             # Hostname, timezone, şifre, reboot, factory reset
│   │   ├── update.go             # OTA update: GitHub Releases + 60s watchdog rollback
│   │   ├── safefetch.go          # SSRF korumalı paylaşılan HTTP client'ları
│   │   ├── tls.go                # Sertifika modları: self-signed yeniden üretme, mkcert
│   │   ├── acme.go               # ACME RFC 8555 akışı + 30 gün kala yenileme döngüsü
│   │   ├── acme_dns.go           # DNS-01 sağlayıcıları: Cloudflare API + manual
│   │   ├── firstboot.go          # İlk açılış br0 köprüsü (serve.go içinde devrede)
│   │   ├── backup.go             # Config export/import + scrypt AES-GCM pipeline
│   │   ├── backup_local.go       # Local hedef (/var/lib/lankeeper/backups/ whitelist)
│   │   ├── backup_s3.go          # S3-uyumlu hedef, native SigV4 (aws-sdk-go YOK)
│   │   ├── backup_sftp.go        # SFTP hedef, PosixRename ile atomic overwrite
│   │   ├── backup_schedule.go    # Cron parser (@aliases + 5 alan, Vixie DOM/DOW)
│   │   ├── backup_orchestration.go # Scheduler goroutine + RunNow(ctx) runMu altında
│   │   ├── metrics.go            # Nil-safe MetricsSnapshot composer
│   │   ├── metrics_collectors.go # Servislerden read-only state toplama
│   │   └── metrics_exposition.go # Exposition format 0.0.4 writer (stdlib fmt.Fprintf)
│   ├── i18n/
│   │   ├── i18n.go               # Locale yükleme, T() ve WithParams() fonksiyonları
│   │   ├── default.go            # Varsayılan dil sabitleri
│   │   └── middleware.go         # Dil tespiti middleware (cookie → Accept-Language → default)
│   ├── netutil/
│   │   ├── atomic.go             # AtomicChange struct + rollback logic
│   │   ├── exec.go               # Run/RunSimple — agent IPC proxy veya lokal os/exec
│   │   ├── agenterr.go           # Agent hata tipleri
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
│   │   ├── pages/                # 18 sayfa
│   │   │   ├── dashboard.html
│   │   │   ├── network.html      # Interface + VLAN + PPPoE + USB + health tek sayfada
│   │   │   ├── firewall.html
│   │   │   ├── dns.html
│   │   │   ├── dhcp.html
│   │   │   ├── ipv6.html         # DHCPv6-PD + 6in4 tunnel + subnet map
│   │   │   ├── qos.html
│   │   │   ├── vpn.html
│   │   │   ├── vpn-s2s.html      # Site-to-site kurulum sihirbazı
│   │   │   ├── openvpn.html
│   │   │   ├── routing.html
│   │   │   ├── nas.html
│   │   │   ├── storage.html
│   │   │   ├── syslog.html
│   │   │   ├── ntp.html
│   │   │   ├── backup.html       # Zamanlanmış yedekleme hedefleri + history
│   │   │   ├── settings.html
│   │   │   └── login.html
│   │   └── partials/             # 14 partial
│   │       ├── sidebar.html
│   │       ├── wan-status.html
│   │       ├── pppoe-sniff.html
│   │       ├── vlan_list.html
│   │       ├── healthcheck.html
│   │       ├── dns-blocklist.html
│   │       ├── share_list.html
│   │       ├── m3u-status.html
│   │       ├── raid_status.html
│   │       ├── ntp_status.html
│   │       ├── syslog-server-config.html
│   │       ├── syslog-client-config.html
│   │       ├── syslog-sources.html
│   │       └── syslog-logs.html
│   ├── static/
│   │   ├── css/
│   │   │   ├── reset.css
│   │   │   ├── variables.css     # CSS custom properties (dark/light tema)
│   │   │   ├── layout.css
│   │   │   ├── components.css
│   │   │   └── pages.css
│   │   └── js/
│   │       ├── htmx.min.js       # HTMX library (~14KB gzip)
│   │       ├── htmx-sortable.js  # Drag-drop extension (PBR sıralama)
│   │       ├── chart.js          # Canvas-based grafik helper
│   │       ├── qos-chart.js      # Per-client bandwidth SSE tablosu + sparkline
│   │       ├── vpn-s2s.js        # S2S sihirbazı adım geçişleri
│   │       ├── qrcode.js         # ISO/IEC 18004 encoder (byte mode, v1-40, EC L/M)
│   │       ├── qr-modal.js       # QR modal'ı: fetch + canvas, delegasyonlu dinleyici
│   │       └── app.js            # Tema toggle, chart init
│   ├── locales/
│   │   ├── tr.json               # Türkçe çeviriler (679 anahtar)
│   │   └── en.json               # İngilizce çeviriler (679 anahtar)
│   └── embed.go                  # go:embed ile static + template + locale'leri binary'ye göm
├── configs/
│   ├── sysconf/                  # 17 sistem config şablonu, hepsi servislere bağlı
│   │   ├── nftables.conf.tmpl
│   │   ├── pppoe-peer.tmpl
│   │   ├── pppoe-options.tmpl
│   │   ├── pppoe-server-options.tmpl
│   │   ├── unbound.conf.tmpl
│   │   ├── dnsmasq.conf.tmpl
│   │   ├── dnsmasq-ipv6-ra.conf.tmpl
│   │   ├── dnscrypt-proxy.toml.tmpl
│   │   ├── dhcp6c.conf.tmpl
│   │   ├── dhcp6c-script.tmpl
│   │   ├── rsyslog.conf.tmpl
│   │   ├── chrony.conf.tmpl
│   │   ├── wireguard-server.conf.tmpl
│   │   ├── wireguard-client.conf.tmpl
│   │   ├── openvpn-server.conf.tmpl
│   │   ├── openvpn-client.conf.tmpl
│   │   └── smb.conf.tmpl
│   └── defaults/                 # go:embed ile gömülür — factory reset buradan geri yükler
│       ├── router.yaml
│       ├── firewall.yaml
│       ├── qos.yaml
│       ├── vpn.yaml
│       ├── routing.yaml
│       └── nas.yaml
├── deploy/
│   ├── systemd/
│   │   ├── lankeeper.target          # Orchestration target
│   │   ├── lankeeper-agent.service   # Root agent
│   │   ├── lankeeper-web.service     # Unprivileged web (Restart=always, RestartSec=3)
│   │   └── lankeeper-dhcp6c.service  # wide-dhcpv6 client (Conflicts=wide-dhcpv6-client.service)
│   ├── install.sh                # Tam kurulum scripti (Debian 12 üzerine)
│   ├── factory-reset.sh          # Fabrika ayarlarına dönüş
│   ├── backup.sh                 # Cron backup scripti
│   ├── dhcp-dns-update.sh        # DHCP lease → DNS kaydı senkronizasyon hook'u
│   └── iso/
│       ├── Dockerfile.build      # ISO builder image (macOS'ta xorriso yok)
│       ├── build-iso.sh          # Debian preseed ISO oluşturma
│       ├── preseed.cfg           # Debian unattended install preseed
│       ├── post-install.sh       # Preseed sonrası kurulum
│       ├── grub.cfg              # UEFI/BIOS dual-boot GRUB config
│       ├── debian-images.sha512  # Kaynak ISO checksum pinleri
│       ├── verify_source_iso_test.go  # TEST-ONLY: kaynak ISO doğrulaması
│       └── ssh_root_access_test.go    # TEST-ONLY: installer script guard'ları
├── buildsys/                     # TEST-ONLY paket, üretim kodu yok
│   ├── checksums_test.go         # Release checksum üretimi
│   ├── artifact_names_test.go    # README + Makefile artefakt adı tutarlılığı
│   ├── csprng_test.go            # CSPRNG kullanımı
│   ├── workflow_pins_test.go     # CI action SHA pinleri
│   ├── iso_mounts_test.go        # ISO builder bind mount şekli
│   ├── gomod_markers_test.go     # go.mod direct/indirect require blokları
│   ├── formatting_test.go        # gofmt + .golangci.yml formatter girdisi
│   └── qr_assets_test.go         # QR encoder lisans başlığı, ağ/HTML sink yokluğu, şablon bağları
├── .github/
│   └── workflows/
│       └── ci.yml                # 7 gate: build, test, vet, lint, govulncheck, gosec, cross-all
├── .golangci.yml                 # Upstream default linter seti + gofmt formatter
├── go.mod
├── go.sum
├── Makefile                      # build, test, lint, cross, iso, release, install, check, clean
├── CLAUDE.md                     # Claude Code rehberi (gitignore'da)
├── AGENTS.md                     # Diğer AI agent'lar için aynı rehber (gitignore'da)
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

### Tam Offline Web UI — Harici Bağımlılık Yok

Web UI internet bağlantısı olmadan %100 çalışır. Hiçbir harici kaynak (CDN, Google Fonts, external script/style) yüklenmez.

**Gömülü tüm varlıklar:**
- `htmx.min.js` — HTMX kütüphanesi (~14KB gzip), CDN'den değil binary'den servis
- `htmx-sortable.js` — Drag-and-drop extension (PBR politika sıralama)
- `variables.css`, `layout.css`, `components.css`, `pages.css` — tüm stil dosyaları
- `app.js`, `chart.js` — minimal vanilla JS helper'ları
- SVG ikonlar — `web/static/icons/` altında, harici ikon fontu yok (Font Awesome, Material Icons vb. yok)
- Fontlar: sistem font stack (`-apple-system`, `Segoe UI`, vb.) — harici font indirme yok

**Neden offline:**
- Router, internet bağlantısını yöneten cihaz — WAN koptuğunda bile yönetim arayüzü erişilebilir olmalı
- CDN bağımlılığı = single point of failure, gizlilik riski, yavaş LAN yükleme
- `go:embed` ile tüm varlıklar binary içinde — ek dosya/dizin gerekmez

**Content-Security-Policy header:**
```
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'
```
Harici kaynak yüklenmesini CSP header ile de zorla engelle — XSS ile bile dışarıdan script yüklenemez.

---

## Config Schema (router.yaml)

```yaml
system:
  hostname: "lankeeper"
  timezone: "Europe/Istanbul"
  language: "tr"                         # tr | en (varsayılan dil, cookie override eder)
  adminPasswordHash: "$2a$12$..."       # bcrypt
  sessionSecret: "..."                   # 32-byte hex, cookie signing
  webPort: 8443
  webBind: "10.10.10.1"                   # Sadece LAN
  tls:
    mode: "self-signed"                  # self-signed | acme | mkcert
    certFile: ""                         # Custom sertifika yolu (boş = otomatik)
    keyFile: ""                          # Custom anahtar yolu (boş = otomatik)
    selfSigned:
      cn: "lankeeper.lan"              # Common Name
      validDays: 3650                    # Geçerlilik süresi (10 yıl)
      sans:                              # Subject Alternative Names
        - "lankeeper.lan"
        - "10.10.10.1"
        - "router.local"
    acme:                                  # Let's Encrypt (ACME)
      enabled: false
      email: ""                          # ACME hesap e-posta
      domain: ""                         # Public domain (ör: router.example.com)
      provider: "letsencrypt"            # bilgilendirme; CA seçimi directoryUrl ile
      directoryUrl: ""                   # boş = Let's Encrypt STAGING (production haftada 5 ile sınırlı)
      dnsChallenge:                      # DNS-01 challenge (LAN-only router için HTTP-01 çalışmaz)
        provider: ""                     # cloudflare | manual (route53 kapsam dışı)
        apiToken: "enc:v1:..."           # AES-256-GCM, forma geri yazılmaz
    mkcert:
      caInstalled: false                 # mkcert CA'sı cihazlara yüklenmiş mi (bilgilendirme)
      sans:                              # Son mkcert sertifikasının adları
        - "lankeeper.lan"
        - "10.10.10.1"

interfaces:
  - id: "wan"                            # Sistem içi tanımlayıcı (değiştirilemez)
    device: "enp3s0"                     # Fiziksel NIC (udev rule ile sabitlenmiş)
    label: "WAN Fiber"                   # Kullanıcı tarafından verilen görünen isim
    role: "wan"                          # wan | lan | unused
    type: "pppoe"
    mtu: 1492
    mac: "aa:bb:cc:dd:ee:01"            # Otomatik algılanan MAC
    ipv6: "auto"                         # auto (DHCPv6-PD/SLAAC), manual, off
  - id: "lan"
    device: "enp0s25"
    label: "Ev Ağı"
    role: "lan"
    type: "static"
    address: "10.10.10.1/24"
    address6: ""                         # DHCPv6-PD prefix'den otomatik atanır (ör: 2001:db8:1::1/64)
    mtu: 1500
    mac: "aa:bb:cc:dd:ee:02"

vlans:                                     # 802.1Q VLAN tanımları
  - id: "iptv"                             # Sistem içi tanımlayıcı
    parent: "wan"                          # Üst interface id (fiziksel NIC'in id'si)
    vid: 10                                # VLAN ID (1-4094)
    label: "IPTV VLAN"                     # Kullanıcı tarafından verilen isim
    role: "wan"                            # wan | lan | unused
    type: "static"                         # static | dhcp-client
    address: ""                            # dhcp-client ise boş
    mtu: 1500
  - id: "guest"
    parent: "lan"                          # LAN NIC üzerinde VLAN
    vid: 100
    label: "Misafir Ağı"
    role: "lan"
    type: "static"
    address: "10.10.13.1/24"              # Ayrı subnet
    mtu: 1500
    isolated: true                         # Ana LAN'dan izole (inter-VLAN routing yok)
    dhcp:                                  # Bu VLAN için ayrı DHCP havuzu
      enabled: true
      rangeStart: "10.10.13.100"
      rangeEnd: "10.10.13.250"
      leaseTime: "6h"

healthCheck:
  enabled: true
  checks:
    - name: "wan-internet"
      interface: "wan"                     # Interface id
      targets:                             # Kontrol hedefleri (en az 1'i başarılı = OK)
        - type: "ping"
          host: "8.8.8.8"
        - type: "ping"
          host: "1.1.1.1"
        - type: "http"
          url: "http://connectivitycheck.gstatic.com/generate_204"
          expectStatus: 204
      interval: "30s"                      # Kontrol aralığı
      timeout: "5s"                        # Tek kontrol timeout'u
      failureThreshold: 3                  # Kaç ardışık başarısızlıkta aksiyon al
      failureWindow: "5m"                  # Başarısızlık penceresi (3/5dk gibi)
      actions:                             # Sırayla denenecek aksiyonlar
        - type: "restartInterface"         # Interface'i restart et
          delay: "0s"
        - type: "restartPppoe"             # PPPoE bağlantısını yeniden kur
          delay: "30s"                     # Önceki aksiyon sonrası bekleme
        - type: "failoverUsb"             # USB tethering'e geç (telefon bağlıysa)
          delay: "10s"
        - type: "rebootSystem"             # Son çare: sistemi yeniden başlat
          delay: "120s"
      cooldown: "5m"                       # Aksiyon sonrası yeniden kontrol bekleme
      notify: true                         # Web UI + syslog'a bildirim

pppoe:
  username: "..."                        # .credentials.enc'den okunur
  password: "..."
  mtu: 1492
  mru: 1492
  lcpEchoInterval: 10
  lcpEchoFailure: 3
  persist: true
  holdoff: 5
  ipv6cp: true                           # IPv6CP negotiation etkinleştir (+ipv6 pppd seçeneği)

usbTethering:
  enabled: false                           # USB tethering desteği (Android telefon)
  autoFailover: true                       # PPPoE düşünce otomatik USB'ye geç
  autoFailback: true                       # PPPoE geri gelince otomatik ana bağlantıya dön
  failoverDelay: "10s"                     # PPPoE fail → USB geçiş bekleme süresi
  failbackDelay: "30s"                     # PPPoE geri geldi → ana bağlantıya dönüş bekleme
  interface: "usb0"                        # Tethering interface adı (genelde usb0 veya rndis0)
  metric: 100                              # Route metric (PPPoE: 0, USB: 100 → PPPoE öncelikli)
  nat: true                                # USB interface üzerinden NAT masquerade
  ttlFix: true                             # USB üzerinden çıkan paketlerde TTL sabitleme (tethering tespiti)

firewall:
  defaultPolicy: "drop"                 # WAN input/forward
  ttlFix:
    enabled: false                       # TTL sabitleme (ISP tethering tespitini atlatır)
    value: 64                            # Sabitlenecek TTL değeri (64 = Linux default)
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
  queryLog:
    enabled: true                        # DNS sorgu loglama (Unbound verbosity: 2)
    logPath: "/var/log/unbound/queries.log"  # Sorgu log dosyası
    maxSize: "100M"                      # Log dosyası boyut limiti (logrotate)
    retention: "7d"                      # Log saklama süresi
    logBlocked: true                     # Engellenen sorguları ayrıca işaretle

dhcp:
  rangeStart: "10.10.10.100"
  rangeEnd: "10.10.10.250"
  leaseTime: "12h"
  gateway: "10.10.10.1"
  dnsServer: "10.10.10.1"                  # Unbound'a yönlendir
  staticLeases:
    - mac: "aa:bb:cc:dd:ee:ff"
      ip: "10.10.10.10"
      hostname: "desktop"

ipv6:
  enabled: "auto"                          # auto | on | off — auto: ISP IPv6CP başarılırsa etkinleşir
  mode: "dhcpv6-pd"                        # dhcpv6-pd | 6in4 | static | slaac — WAN tarafı IPv6 alma yöntemi
  wan:
    acceptRA: true                         # Router Advertisement kabul et (ISP'den)
    requestPrefix: true                    # DHCPv6-PD ile prefix talep et
    prefixHint: "/56"                      # ISP'den istenen prefix uzunluğu (/48, /56, /64)
  lan:
    mode: "slaac"                          # slaac | dhcpv6-stateless | dhcpv6-stateful
    ula:
      enabled: true                        # Unique Local Address (ISP prefix olmasa da LAN içi IPv6)
      prefix: "fd00:abcd:1234::/48"        # ULA prefix (otomatik üretilebilir)
    raInterval: 30                         # Router Advertisement gönderim aralığı (saniye)
    rdnss: true                            # RA ile DNS sunucu bilgisi (RDNSS option)
  tunnel:                                  # mode: "6in4" iken kullanılır — dhcpv6-pd ile mutually exclusive
    provider: "he.net"                     # Bugün sabit; ileride başka broker'lar için alan hazır
    serverIPv4: "216.66.80.30"             # Tunnel broker POP IPv4
    clientIPv6: "2001:470:1f0a:abc::2/64"  # Point-to-point tunnel /64'ünün bizim ucumuz
    routedPrefix: "2001:470:1f0b:abc::/64" # LAN'a dağıtılan ayrı Routed /64 veya /48
    tunnelID: "123456"                     # HE.net tunnel ID (DDNS update hostname'i)
    username: "heuser"                     # HE.net kullanıcı adı (Basic Auth)
    updateKey: "..."                       # /nic/update Basic Auth parolası (plaintext, PPPoE.Password ile aynı seviye)
  privacy: true                            # RFC 4941 — temporary address (privacy extensions) önerisi RA'da

vpn:
  clients:                                 # Outbound VPN client tünelleri (dış VPN sunuculara bağlanma)
    - name: "nl-amsterdam"
      endpoint: "1.2.3.4:51820"
      privateKey: "..."                  # .credentials.enc
      publicKey: "..."
      allowedIPs: "0.0.0.0/0, ::/0"       # Dual-stack full tunnel
      dns: "10.10.10.1"
      table: 100
      fwmark: 100
  server:                                  # Inbound VPN server (eve dışarıdan bağlanma)
    enabled: false
    listenPort: 51820
    privateKey: "..."                    # .credentials.enc (ilk kurulumda otomatik üretilir)
    publicKey: "..."
    address: "10.10.11.1/24"             # VPN server subnet (IPv4)
    address6: "fd10:10::1/64"           # VPN server subnet (IPv6 ULA)
    dns: "10.10.10.1"                     # Client'lara verilecek DNS
    postUp: ""                          # Opsiyonel custom komut
    postDown: ""
    mtu: 1420                           # PPPoE altı: 1492 - 60 (WG overhead) - 12 (margin)
    peers:                               # Bağlanacak uzak cihazlar (road warrior)
      - name: "telefon"
        publicKey: "..."
        presharedKey: "..."              # .credentials.enc (opsiyonel, ekstra güvenlik)
        allowedIPs: "10.10.11.2/32"      # Peer'a atanan IP
        keepalive: 25                    # NAT traversal (saniye, 0=kapalı)
      - name: "laptop"
        publicKey: "..."
        presharedKey: "..."
        allowedIPs: "10.10.11.3/32"
        keepalive: 25
  deviceAssignments:
    "aa:bb:cc:dd:ee:ff": "nl-amsterdam"

openvpn:
  clients:                                 # Outbound OpenVPN client bağlantıları
    - name: "work-vpn"
      configFile: ""                     # .ovpn dosya içeriği (import ile yüklenir)
      username: "..."                    # .credentials.enc (opsiyonel, auth-user-pass)
      password: "..."
      autoConnect: false                 # Başlangıçta otomatik bağlan
      table: 200                         # PBR routing table
      fwmark: 200
  server:
    enabled: false
    protocol: "udp"                      # udp | tcp
    port: 1194
    device: "tun"                        # tun | tap
    subnet: "10.10.12.0/24"              # VPN server subnet (IPv4)
    subnet6: "fd20:20::/64"             # VPN server subnet (IPv6 ULA)
    dns: "10.10.10.1"
    cipher: "AES-256-GCM"
    auth: "SHA256"
    tlsAuth: true                        # tls-auth HMAC (ekstra güvenlik katmanı)
    compression: false                   # Güvenlik riski — varsayılan kapalı
    maxClients: 10
    keepalive: "10 120"                  # ping 10, ping-restart 120
    clientToClient: false                # Client'lar arası trafik
    duplicateCn: false                   # Aynı CN ile çoklu bağlantı
    clients:                             # Sertifika bazlı client tanımları
      - name: "is-laptop"
        commonName: "is-laptop"          # Sertifika CN
        fixedIP: "10.10.12.2"             # Sabit IP (opsiyonel, boş = havuzdan)
        enabled: true
      - name: "tablet"
        commonName: "tablet"
        fixedIP: ""
        enabled: true
    ccd: {}                              # Client-specific config override (opsiyonel)

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

syslog:
  server:
    enabled: false                       # Syslog sunucu (ağdan log alma)
    listenUDP: ":514"                    # UDP dinleme adresi
    listenTCP: ":514"                    # TCP dinleme adresi (boş = kapalı)
    enableTLS: false                     # TLS ile TCP (sertifika gerekli)
    logPath: "/var/log/remote"           # Uzak logların yazılacağı dizin
    perHostDirs: true                    # Her kaynak IP için ayrı dizin
    maxRetention: "30d"                  # Log saklama süresi
  client:
    enabled: false                       # Logları harici sunucuya ilet
    remoteHost: ""                       # Hedef sunucu (örn: "192.168.1.100:514")
    protocol: "udp"                      # udp | tcp | relp
    enableTLS: false
    facilities:                          # İletilecek facility'ler
      - "kern.*"
      - "auth.*"
      - "daemon.*"

ntp:
  server:
    enabled: true                        # LAN cihazlarına NTP sunuculuğu
    listenAddress: "10.10.10.1"            # Sadece LAN interface
    listenPort: 123
  client:
    enabled: true                        # Router'ın kendi zaman senkronizasyonu
    sources:                             # Upstream NTP sunucuları (sıralı)
      - "0.tr.pool.ntp.org"
      - "1.tr.pool.ntp.org"
      - "2.pool.ntp.org"
      - "3.pool.ntp.org"
    fallback: "time.google.com"          # Pool'lar ulaşılamaz ise
  rtcSync: true                          # Sistem saatini RTC'ye yaz (hwclock)
  allowSubnets:                          # NTP sunucuya erişim izni
    - "10.10.10.0/24"                      # LAN
    - "10.10.11.0/24"                     # VPN server peer'ları

storage:
  raid:
    device: "/dev/md0"
    level: 1
    members: ["/dev/sda1", "/dev/sdb1"]
  smartCheckInterval: 3600

routing:                                   # Policy-based routing politikaları
  policies:
    - name: "kids-vpn"
      enabled: true
      priority: 100                        # Küçük değer önce değerlendirilir
      srcMacs: ["aa:bb:cc:dd:ee:ff"]
      srcIps: []
      dstIps: []
      dstPorts: []
      domains: []
      protocol: ""                         # "" | tcp | udp
      tunnel: "nl-amsterdam"               # Hedef VPN tüneli
      killSwitch: true                     # Tünel düşerse trafiği düşür, WAN'a sızdırma
      schedule: ""                         # Boş = her zaman aktif

backup:                                    # Zamanlanmış şifreli yedekleme
  enabled: true
  schedule: "@daily"                       # @hourly | @daily | @weekly | @monthly | @yearly | 5 alan cron
  passphrase: "enc:v1:..."                 # AES-256-GCM at rest (secrets.go), boş form alanı mevcut değeri korur
  retention: 7                             # Hedef başına saklanacak en yeni N arşiv
  targets:
    - type: "local"                        # local | s3 | sftp
      name: "yerel"
      path: "/var/lib/lankeeper/backups/"  # local hedef bu dizine whitelist'li
    - type: "s3"
      name: "minio"
      endpoint: "https://s3.example.com"
      region: "us-east-1"
      bucket: "lankeeper"
      prefix: "router/"
      accessKeyId: "enc:v1:..."
      secretAccessKey: "enc:v1:..."
      usePathStyle: true                   # MinIO / B2 / Spaces için
    - type: "sftp"
      name: "nas"
      host: "10.10.10.20"
      port: 22
      user: "backup"
      password: "enc:v1:..."               # veya keyPath
      keyPath: ""
      remoteDir: "/backups/lankeeper"
      hostKeyFingerprint: "SHA256:..."     # Boşken bağlantı REDDEDİLİR; sır değil, şifrelenmez
  lastRun: "2026-08-07T03:00:00Z"          # Çalışma zamanı yazılır
  lastStatus: "ok"                         # ok | partial | error
  history: []                              # 50 girişlik ring buffer
```

**Not:** `system.tls`, `interfaces`, `vlans`, `healthCheck`, `pppoe`, `usbTethering`, `firewall`, `qos`, `dns`, `dhcp`, `ipv6`, `vpn`, `openvpn`, `nas`, `syslog`, `ntp`, `storage`, `routing`, `backup` — toplam 19 üst düzey anahtar `internal/config/config.go` içindeki `Config` struct'ı ile birebir eşleşir. OTA update durumu config'de DEĞİL, `/var/lib/lankeeper/update-state.json` içinde tutulur (restart'ta korunur).

---

## Route + Handler Inventory

`internal/web/server.go` `setupRoutes()` içinde **118 route** kayıtlı. Uygulama, tasarım aşamasında planlanan `/partials/*` şemasını kullanMAdı: partial'lar ilgili sayfa handler'ı içinde render edilir, ayrı GET endpoint'i açılmaz. Liste `grep -oE '"(GET|POST|PUT|DELETE) [^"]+"' internal/web/server.go` çıktısıyla senkron tutulur.

**Kimlik doğrulaması istemeyen route'lar:** `GET /static/`, `GET /login`, `POST /login`, `POST /logout`, `POST /settings/lang`, `GET /api/version`, `GET /metrics`. Geri kalan her şey `AuthRequired` sarmalı içindedir. `POST /settings/lang` auth'suz olarak durum değiştirir.

### Auth + i18n + Genel

| Method | Path           | Açıklama                                     |
|--------|----------------|----------------------------------------------|
| GET    | /login         | Login formu                                  |
| POST   | /login         | Oturum aç (ayrı sıkı rate limit: 5s'de bir)  |
| POST   | /logout        | Oturum kapat                                 |
| POST   | /settings/lang | Dil değiştir (auth GEREKMEZ)                 |
| GET    | /static/       | Gömülü CSS/JS varlıkları                     |
| GET    | /api/version   | Sürüm bilgisi (auth GEREKMEZ, OTA istemcisi) |
| GET    | /metrics       | Prometheus exposition (auth GEREKMEZ, LAN-only) |

### Dashboard + SSE

| Method | Path          | Açıklama                                  |
|--------|---------------|-------------------------------------------|
| GET    | /{$}          | Dashboard tam sayfa                       |
| GET    | /events/stats | SSE: sistem metrikleri stream             |
| GET    | /events/qos   | SSE: per-client bandwidth stream          |

### Network / VLAN / PPPoE / Health Check

| Method | Path                                | Açıklama                            |
|--------|-------------------------------------|-------------------------------------|
| GET    | /network                            | Interface + VLAN + PPPoE + USB + health tek sayfa |
| POST   | /network/interface                  | Arayüz ekle/güncelle (rol + tip doğrulaması) |
| DELETE | /network/interface/{id}             | Arayüz sil (son LAN arayüzü reddedilir) |
| POST   | /network/vlan                       | VLAN ekle                           |
| DELETE | /network/vlan/{id}                  | VLAN sil                            |
| POST   | /network/pppoe/connect              | PPPoE bağlan                        |
| POST   | /network/pppoe/disconnect           | PPPoE bağlantısını kes              |
| POST   | /network/pppoe/sniff/start          | Credential yakalama başlat          |
| POST   | /network/pppoe/sniff/stop           | Credential yakalama durdur          |
| POST   | /network/healthcheck/{name}/reset   | Failure counter sıfırla             |
| GET    | /network/healthcheck/status         | Health kartı partial (10s'de bir tazelenir) |
| POST   | /network/usb/enable                 | USB tethering'i etkinleştir         |
| POST   | /network/usb/disable                | USB tethering'i devre dışı bırak    |
| POST   | /network/usb/activate               | USB rotasına geç (telefon yoksa 400) |
| POST   | /network/usb/deactivate             | USB rotasından çık                  |
| POST   | /network/usb/auto-failover          | Otomatik yedeklemeyi aç/kapat       |
| POST   | /network/first-boot/complete        | İlk açılış kurulumunu bitir (bayrağı sil) |

### Firewall

| Method | Path                                  | Açıklama                          |
|--------|---------------------------------------|-----------------------------------|
| GET    | /firewall                             | Firewall sayfası                  |
| POST   | /firewall/rules                       | Kural ekle                        |
| DELETE | /firewall/rules/{index}               | Kural sil                         |
| POST   | /firewall/rules/{index}/toggle        | Kuralı etkinleştir/devre dışı     |
| POST   | /firewall/open-ports                  | Açık port ekle                    |
| DELETE | /firewall/open-ports/{index}          | Açık port sil                     |
| POST   | /firewall/open-ports/{index}/toggle   | Açık portu etkinleştir/devre dışı |
| POST   | /firewall/port-forwards               | Port yönlendirme ekle             |
| DELETE | /firewall/port-forwards/{index}       | Port yönlendirme sil              |
| POST   | /firewall/ttl-fix                     | TTL fix aç/kapat + değer (1-255)  |
| POST   | /firewall/apply                       | Ruleset uygula (30s watchdog arm) |
| POST   | /firewall/confirm                     | Watchdog onayı                    |
| POST   | /firewall/rollback                    | Manuel geri alma                  |

### DNS (Unbound + DoT/DoH)

| Method | Path                     | Açıklama                                    |
|--------|--------------------------|---------------------------------------------|
| GET    | /dns                     | DNS ayarları + istatistik + blocklist       |
| POST   | /dns/records             | Statik DNS kaydı ekle                       |
| DELETE | /dns/records/{index}     | Statik DNS kaydı sil                        |
| POST   | /dns/blocklist/update    | Blocklist'i şimdi güncelle                  |
| POST   | /dns/clear-log           | Query log geçmişini temizle                 |
| POST   | /dns/dot                 | Şifreleme modunu ayarla (plain/DoT/DoH)     |
| POST   | /dns/dot/probe           | DoT upstream canlılık testi (1s'de bir limit)|
| POST   | /dns/doh/probe           | DoH upstream canlılık testi (1s'de bir limit)|

### DHCP (dnsmasq)

| Method | Path                    | Açıklama                                  |
|--------|-------------------------|-------------------------------------------|
| GET    | /dhcp                   | Lease listesi + ayarlar                   |
| POST   | /dhcp/static            | Statik lease ekle (DNS kaydına aynalanır) |
| DELETE | /dhcp/static/{index}    | Statik lease sil                          |

### IPv6 (DHCPv6-PD + 6in4)

| Method | Path                  | Açıklama                                |
|--------|-----------------------|-----------------------------------------|
| GET    | /ipv6                 | Status + Config + Subnet Map + Tunnel    |
| POST   | /ipv6/save            | IPv6 ayarlarını kaydet                  |
| POST   | /ipv6/start           | IPv6 planını başlat                     |
| POST   | /ipv6/stop            | IPv6 planını durdur                     |
| POST   | /ipv6/renew           | DHCPv6 prefix renew                     |
| POST   | /ipv6/release         | DHCPv6 prefix release                   |
| POST   | /ipv6/subnet-map      | LAN/VLAN sub-prefix atamalarını kaydet  |
| POST   | /ipv6/tunnel/update   | HE.net /nic/update DDNS çağrısı         |

### QoS

| Method | Path        | Açıklama                        |
|--------|-------------|---------------------------------|
| GET    | /qos        | QoS ayarları + per-client tablo |
| POST   | /qos/apply  | CAKE qdisc + IFB uygula         |
| POST   | /qos/clear  | Shaping'i kaldır                |

### WireGuard VPN

| Method | Path                             | Açıklama                              |
|--------|----------------------------------|---------------------------------------|
| GET    | /vpn                             | Client tünelleri + server + peer'lar   |
| POST   | /vpn/client/{name}/connect       | Client tünelini bağla                 |
| POST   | /vpn/client/{name}/disconnect    | Client tünelini kes                   |
| POST   | /vpn/server/start                | WG server başlat                      |
| POST   | /vpn/server/stop                 | WG server durdur                      |
| POST   | /vpn/server/peer                 | Peer ekle (keypair otomatik üretilir) |
| GET    | /vpn/server/peer/{name}/config   | Peer config'i yeniden indir (no-store) |
| DELETE | /vpn/server/peer/{name}          | Peer sil                              |

### WireGuard Site-to-Site

| Method | Path                            | Açıklama                                |
|--------|---------------------------------|-----------------------------------------|
| GET    | /vpn/s2s                        | S2S kurulum sihirbazı                   |
| POST   | /vpn/s2s/invite                 | HMAC-imzalı invite token üret           |
| POST   | /vpn/s2s/join                   | Karşı taraftan gelen invite'ı kabul et  |
| POST   | /vpn/s2s/finalize               | Ack token ile peer'ı kalıcı hale getir  |
| POST   | /vpn/s2s/rotate-key             | Token imzalama anahtarını döndür        |
| GET    | /vpn/s2s/{name}/health          | Peer sağlık durumu                      |
| POST   | /vpn/s2s/{name}/reachability    | Erişilebilirlik testi                   |
| DELETE | /vpn/s2s/{name}                 | S2S peer'ı sil                          |

### OpenVPN

| Method | Path                                   | Açıklama                              |
|--------|----------------------------------------|---------------------------------------|
| GET    | /openvpn                               | Server + client yönetimi              |
| POST   | /openvpn/init-pki                      | easy-rsa PKI oluştur (CA + server)    |
| POST   | /openvpn/server/start                  | Server başlat                         |
| POST   | /openvpn/server/stop                   | Server durdur                         |
| POST   | /openvpn/server/client                 | Client sertifikası üret               |
| DELETE | /openvpn/server/client/{name}          | Client sertifikasını sil              |
| GET    | /openvpn/server/client/{name}/config   | .ovpn indir (Cache-Control: no-store) |
| POST   | /openvpn/client                        | Outbound client profili ekle          |
| POST   | /openvpn/client/{name}/connect         | Client bağlantısını başlat            |
| POST   | /openvpn/client/{name}/disconnect      | Client bağlantısını kes               |

### Policy-Based Routing

| Method | Path                      | Açıklama                          |
|--------|---------------------------|-----------------------------------|
| GET    | /routing                  | PBR politika yönetimi             |
| POST   | /routing/policy           | Politika ekle                     |
| DELETE | /routing/policy/{name}    | Politika sil                      |
| POST   | /routing/reorder          | Sürükle-bırak sıralama            |

### NAS

| Method | Path                        | Açıklama                       |
|--------|-----------------------------|--------------------------------|
| GET    | /nas                        | Paylaşımlar + M3U durumu       |
| POST   | /nas/shares                 | Paylaşım ekle                  |
| DELETE | /nas/shares/{name}          | Paylaşım sil                   |
| POST   | /nas/m3u/sync               | M3U senkronizasyonu başlat     |
| POST   | /nas/m3u/discover-groups    | M3U grup listesini keşfet      |

### Storage / Syslog / NTP

| Method | Path                                  | Açıklama                          |
|--------|---------------------------------------|-----------------------------------|
| GET    | /storage                              | RAID + SMART + disk kullanımı     |
| GET    | /syslog                               | Syslog yapılandırma + loglar      |
| POST   | /syslog/server                        | Sunucu ayarları                   |
| POST   | /syslog/client                        | Client ayarları                   |
| POST   | /syslog/client/facilities             | Facility filtresi ekle            |
| DELETE | /syslog/client/facilities/{index}     | Facility filtresi sil             |
| GET    | /ntp                                  | NTP yapılandırma + durum          |
| POST   | /ntp/settings                         | NTP ayarları                      |
| POST   | /ntp/sources                          | Upstream kaynak ekle              |
| DELETE | /ntp/sources/{index}                  | Upstream kaynak sil               |
| POST   | /ntp/allow                            | Erişim izni verilen subnet ekle   |
| DELETE | /ntp/allow/{index}                    | Subnet izni sil                   |
| POST   | /ntp/force-sync                       | Manuel senkronizasyon             |

### Backup (zamanlanmış)

| Method | Path                      | Açıklama                        |
|--------|---------------------------|---------------------------------|
| GET    | /backup                   | Hedefler + zamanlama + history  |
| GET    | /backup/history           | Çalışma geçmişi tablosu         |
| POST   | /backup/schedule          | Cron zamanlaması + passphrase   |
| POST   | /backup/target            | Hedef ekle (local/s3/sftp)      |
| DELETE | /backup/target/{name}     | Hedef sil                       |
| POST   | /backup/run               | Şimdi çalıştır                  |

### System

| Method | Path                       | Açıklama                                    |
|--------|----------------------------|---------------------------------------------|
| GET    | /settings                  | Sistem ayarları                             |
| POST   | /settings/hostname         | Hostname değiştir (RFC 1123 doğrulama)      |
| POST   | /settings/timezone         | Timezone değiştir (tz-database doğrulama)   |
| POST   | /settings/web-password     | Web UI şifresi değiştir                     |
| POST   | /settings/root-password    | Sistem root şifresi değiştir                |
| POST   | /settings/tls/regenerate   | Self-signed sertifikayı yeniden üret        |
| POST   | /settings/tls/mode         | TLS modunu değiştir (self-signed / mkcert)  |
| GET    | /settings/tls/ca           | mkcert CA sertifikasını indir (no-store)    |
| POST   | /settings/tls/acme         | ACME yapılandır + ilk sertifikayı al        |
| GET    | /system/backup/export      | Config dışa aktar (Cache-Control: no-store) |
| POST   | /system/backup/import      | Config içe aktar (MaxBytesReader'lı upload) |
| POST   | /system/factory-reset      | Gömülü defaults'tan fabrika ayarları        |
| POST   | /system/reboot             | Sistemi yeniden başlat                      |
| GET    | /system/update/check       | GitHub Releases'te yeni sürüm ara           |
| POST   | /system/update/apply       | Binary'yi değiştir (60s watchdog arm)       |
| POST   | /system/update/confirm     | Güncellemeyi onayla                         |
| POST   | /system/update/rollback    | Önceki binary'ye dön                        |

---

## Go Bağımlılıkları (go.mod)

```
module github.com/KilimcininKorOglu/lankeeper

go 1.26.5

require (
    github.com/fsnotify/fsnotify v1.10.1   // ipv6.go — DHCPv6 lease dosyası izleme
    github.com/gorilla/sessions v1.4.0     // Cookie-based session
    github.com/pkg/sftp v1.13.10           // backup_sftp.go — PosixRename ile atomic overwrite
    golang.org/x/crypto v0.54.0            // bcrypt, scrypt, ssh (SFTP taşıyıcısı), acme (RFC 8555)
    golang.org/x/net v0.56.0               // dns/dnsmessage — DoT/DoH probe
    gopkg.in/yaml.v3 v3.0.1                // Config YAML parse
)

require (
    github.com/gorilla/securecookie v1.1.2 // indirect
    github.com/kr/fs v0.1.0                // indirect
    golang.org/x/sys v0.47.0               // indirect
)
```

**Bilinçli olarak kullanılMAyacaklar:**
- HTTP router framework yok — `net/http.ServeMux` (Go 1.22+ method routing)
- ORM yok — dosya tabanlı config
- Template engine yok — `html/template` (stdlib)
- WebSocket yok — SSE (çok daha basit, HTMX native desteği)
- JSON API yok — HTML partial'lar döner (HTMX paradigması)
- `client_golang` yok — Prometheus exposition 0.0.4 stdlib `fmt.Fprintf` ile (~50 satır)
- `aws-sdk-go` yok — S3 SigV4 imzalama stdlib ile (~150 satır)
- `robfig/cron` yok — cron parser elle yazıldı (alias + 5 alan)

**Toplam harici bağımlılık: 6 direct modül.** Plan başlangıçta 3 öngörüyordu; DoT/DoH probe, SFTP yedekleme hedefi ve DHCPv6 lease izleme sonradan üçünü ekledi. Geri kalan her şey Go stdlib.

`buildsys/gomod_markers_test.go` require bloklarını iki yönlü doğrular: üretim kodunun import ettiği bir modül `// indirect` taşıyamaz, direct blokta duran bir modül de bir yerden import edilmek zorundadır. CI'da `go mod tidy` çalışmadığı için tek denetim budur.

`dnscrypt-proxy` bir Go bağımlılığı DEĞİL, Debian sistem paketidir.

## Sistem Gereksinimleri (install.sh)

```bash
apt-get install -y -qq \
    ppp pppoe \
    nftables \
    wireguard-tools \
    openvpn easy-rsa \
    samba samba-common-bin \
    smartmontools mdadm \
    iproute2 \
    unbound \
    dnsmasq \
    rsyslog \
    chrony \
    qrencode \
    wide-dhcpv6-client \
    dnscrypt-proxy \
    curl \
    jq \
    hdparm

# dnsmasq: DNS kapalı (port=0), sadece DHCP
# unbound: recursive DNS resolver + blocklist
# rsyslog: syslog sunucu (ağdan log alma) + client (log forwarding)
# chrony: NTP sunucu (LAN cihazlarına zaman servisi) + client (upstream senkronizasyon)
# dnscrypt-proxy: DoH upstream stub'ı (127.0.0.1:5353), kurulumda disable edilir
# hdparm: HDD spin-down / spin-up stagger
# Go sadece build makinede gerekli, hedef makinede gerekli DEĞİL
```

**Plan ile fark:**

- `mkcert` artık her iki listede de var ve mod `/settings` sayfasından seçilir. Varsayılan yine `self-signed` ECDSA P-256, otomatik üretilir. ACME ek paket gerektirmez (`golang.org/x/crypto/acme` zaten direct bağımlılık).
- `qrencode` hâlâ kuruluyor ama artık gereksiz: QR üretimi tarayıcıda, `web/static/js/qrcode.js` içinde yapılıyor ve hiçbir Go kodu bu binary'yi çağırmıyor. İki listeden de çıkarılabilir.
- `dnscrypt-proxy`, `curl`, `jq`, `hdparm` plan yazıldıktan sonra eklendi.

ISO kurulum yolu ayrı bir paket listesi kullanır (`LANKEEPER_PACKAGES`, `deploy/iso/build-iso.sh`). İki liste birebir aynı DEĞİLDİR: ISO ayrıca Debian Standard task'ı, `dbus`, `openssh-server` ve `htop` taşır. Yeni bir sistem paketi eklerken İKİ listeyi birden güncelleyin.

## Build & Deploy

Sürümün TEK kaynağı git tag'idir. `Makefile:3` bunu `git describe --tags --always --dirty` ile çözer ve linker `-X main.version=` ile enjekte eder. Bump edilecek bir sürüm dosyası YOKTUR; böyle bir dosya eklemek tag ile çelişebilecek ikinci bir kaynak yaratır.

| Hedef | Karşılığı |
|---|---|
| `make dev` | Sürümsüz hızlı build → `dist/lankeeper` |
| `make build` | version/commit/date ldflags ile production build |
| `make test` | `go test ./... -race -count=1` (cache YASAK) |
| `make lint` | `golangci-lint run` (default set + `gofmt` formatter) |
| `make cross` / `cross-all` | `CGO_ENABLED=0` linux/amd64 (+ arm64) |
| `make install` | Host mimarisini algıla, derle, `sudo bash deploy/install.sh` |
| `make check` | Hedefte kurulum ön koşullarını doğrula |
| `make iso` / `iso-all` | Docker ile offline preseed installer ISO |
| `make release` / `release-all` | Arşivler + `SHA256SUMS` (+ ISO'lar) |
| `make clean` | `dist/` temizle, `dist/packages/` cache'ini KORU |

Asla çıplak `go build` / `go test` çalıştırılmaz; tek istisna Makefile'da per-test hedefi olmadığı için alt küme çalıştırmadır:

```bash
go test ./internal/services/ -run TestVPN -race -count=1 -v
```

`.github/workflows/ci.yml` push ve PR'da yedi gate çalıştırır: `go build ./...`, `go test ./... -race -count=1`, `go vet ./...`, `golangci-lint`, `govulncheck ./...`, `gosec ./...` ve `make cross-all` (amd64 artefaktını çalıştırıp damgalanmış sürümünü de doğrular).

Deploy planda `scp` + `systemctl restart` idi. Uygulamada iki gerçek yol var: `make install` (mevcut Debian 12 üzerine) ve `make iso` (sıfırdan unattended kurulum). Ayrıca web UI üzerinden OTA update, GitHub Releases'ten indirip binary'yi atomik değiştirir ve 60 saniyelik watchdog ile geri alır.

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
- `cmd/lankeeper/main.go` — CLI: `serve` ve `agent` subcommand'ları
- `internal/agent/server.go` — Root agent: UDS dinleyici, JSON-RPC 2.0 dispatcher
- `internal/agent/client.go` — Agent IPC client (web'den kullanılır)
- `internal/agent/operations.go` — Op whitelist registry
- `internal/config/config.go` — YAML config struct + load/save
- `internal/config/crypto.go` — AES-256-GCM encrypt/decrypt
- `internal/config/tls.go` — TLS sertifika yönetimi (self-signed, ACME, mkcert)
- `internal/config/defaults.go` — Varsayılan config
- `internal/config/validate.go` — Config doğrulama
- `internal/i18n/i18n.go` — Locale yükleme, T(), WithParams()
- `internal/i18n/middleware.go` — Dil tespiti middleware (cookie → Accept-Language → default)
- `internal/netutil/atomic.go` — AtomicChange struct
- `internal/netutil/exec.go` — Güvenli exec.Command wrapper
- `web/locales/tr.json` — Türkçe çeviriler (tüm anahtarlar)
- `web/locales/en.json` — İngilizce çeviriler (tüm anahtarlar)
- `configs/defaults/router.yaml` — Varsayılan config dosyası
- `deploy/systemd/lankeeper-agent.service`
- `deploy/systemd/lankeeper-web.service`
- `deploy/systemd/lankeeper.target`
- `deploy/install.sh`

Adımlar:
1. ✅ `go mod init`, Makefile (build/test/lint)
2. ✅ CLI: `cobra` kullanmadan stdlib `flag` + subcommand dispatch
3. ✅ Config: YAML struct (`Language` field dahil), atomic file write (tmp→fsync→rename)
4. ✅ AES-256-GCM: credential encrypt/decrypt (Go `crypto/aes` + `crypto/cipher`)
5. ✅ **i18n paketi:** JSON locale dosyalarını `embed.FS`'den yükle, `T(lang, key)` ve `WithParams(lang, key, params)` fonksiyonları
6. ✅ **i18n middleware:** request'ten dil tespit et (cookie → Accept-Language → config default), `context.WithValue` ile handler'lara ilet
7. ✅ **Locale JSON dosyaları:** `tr.json` ve `en.json` — tüm UI anahtarları (nav, dashboard, pppoe, firewall, vpn, qos, nas, storage, settings, common, auth)
8. ✅ Agent server: `net.Listen("unix", socketPath)` + goroutine per connection
9. ✅ JSON-RPC 2.0 protocol: `{"method": "pppoe.connect", "params": {...}, "id": 1}`
10. ✅ Agent client: dial UDS, send request, read response, timeout
11. ✅ Op whitelist: yalnızca kayıtlı method'lar çalışır
12. ✅ systemd unit dosyaları
13. ✅ **`install.sh` — Tam kapsamlı kurulum scripti:**
    - Root kontrolü + Debian 12 doğrulama
    - Sistem bağımlılıkları: `apt install` ile tüm paketler (nftables, wireguard-tools, unbound, dnsmasq, chrony, samba, openvpn, easy-rsa, rsyslog, ppp, mkcert, wide-dhcpv6-client, qrencode, smartmontools, mdadm)
    - `lankeeper` sistem kullanıcısı oluştur (nologin, /opt/lankeeper home)
    - Binary'yi `/usr/local/bin/lankeeper` altına kopyala + `chmod +x`
    - systemd unit dosyalarını `/etc/systemd/system/` altına yerleştir + `systemctl enable`
    - udev rules: NIC isimlendirme (MAC tabanlı), USB tethering RNDIS → `/etc/udev/rules.d/`
    - Config dizini: `/etc/lankeeper/` oluştur, varsayılan YAML config'leri kopyala
    - Veri dizini: `/var/lib/lankeeper/` (TLS sertifikaları, credentials)
    - Log dizini: `/var/log/lankeeper/`, `/var/log/unbound/`
    - sysctl parametreleri: `/etc/sysctl.d/99-lankeeper.conf` (ip_forward, rp_filter, syncookies, ipv6 forwarding)
    - İlk admin şifresi: interaktif `read -s` ile al → bcrypt hash → config'e yaz
    - İlk TLS sertifikası: self-signed otomatik üretim
    - Unbound/dnsmasq/chrony başlangıç config'leri render
    - `systemctl start lankeeper.target`
    - Kurulum sonrası bilgi: Web UI adresi, SSH notları
14. ✅ Unit test: agent IPC round-trip + i18n T() fonksiyonu + eksik anahtar fallback

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
1. ✅ `net/http.ServeMux` ile routing (Go 1.22+ pattern: `GET /login`, `POST /login`, `POST /settings/lang`)
2. ✅ `html/template` ile layout inheritance: `base.html` → `{{block "content" .}}`
3. ✅ **Template FuncMap'e i18n fonksiyonları ekle:**
   - `t`: `func(lang, key string) string` → çeviri döndür
   - `tp`: `func(lang, key string, params ...string) string` → parametreli çeviri
4. ✅ **Her handler'da `.Lang` context'e ekle:** `data.Lang = i18n.LangFromContext(r.Context())`
5. ✅ **Dil değiştirme handler:** `POST /settings/lang` → `lang` cookie set → `HX-Refresh: true`
6. ✅ **`<html lang="{{ .Lang }}">` attribute'u** base layout'ta dinamik
7. ✅ `go:embed` ile tüm static + template + locale dosyalarını binary'ye göm
8. ✅ **TLS sertifika yönetimi:**
   - `config/tls.go`: TLS modu okuma (self-signed | mkcert | acme)
   - **Self-signed (varsayılan):** ilk başlatmada Go `crypto/ecdsa` (P-256) + `crypto/x509` ile otomatik cert üret
     - SAN: config'deki hostname + LAN IP + `*.local`
     - Sertifika: `/var/lib/lankeeper/tls/server.crt` + `server.key`
     - Geçerlilik: config'den (`selfSigned.validDays`, varsayılan 3650)
   - **mkcert:** `exec.Command("mkcert")` ile sertifika oluştur
     - `mkcert -cert-file {crt} -key-file {key} {hostnames...}`
     - CA sertifikası: `mkcert -CAROOT` → CA dosya yolunu oku → web UI'dan indirilebilir
     - Agent op: `tls.mkcert.generate`, `tls.mkcert.ca_path`
   - **ACME (Let's Encrypt):** `golang.org/x/crypto/acme/autocert` veya `lego`
     - DNS-01 challenge: LAN-only router'da HTTP-01 çalışmaz
     - DNS API token `.credentials.enc`'den çözülür
     - Otomatik yenileme: expire'a 30 gün kala goroutine ile renew
     - Yenileme sonrası `tls.Config.GetCertificate` callback ile sıfır-downtime geçiş
   - `http.Server.TLSConfig`: TLS 1.2+ zorunlu, HSTS yok (LAN IP erişimi bozar)
   - Web UI settings sayfasında: mod seçimi, sertifika durumu (expire tarihi, issuer), yeniden üretme butonu
9. ✅ Session: `gorilla/sessions` ile cookie-based (encrypted, httpOnly, secure, sameSite)
9. ✅ bcrypt ile password verify
10. ✅ Rate limiting: token bucket (stdlib `time.Ticker` + `sync.Map`)
11. ✅ CSRF: double-submit cookie (custom header `X-CSRF-Token`)
12. ✅ LAN-only: middleware'de source IP kontrolü
13. ✅ **HTMX base layout (X design system uygulaması):**
    - Sidebar (sol, 275px): logo + navigasyon (`nav-item` rounded pill, `{{ t .Lang "nav.*" }}`)
    - İçerik (orta, max 600px): sayfa içeriği
    - Panel (sağ, 350px): durum kartları (opsiyonel, dashboard'da aktif)
    - CSS Grid: `grid-template-columns: 275px minmax(0, 600px) 350px`
    - Mobil: sidebar → bottom tab bar (responsive breakpoint)
    - Toast: alt-merkez, slide-up animasyon, 3s auto-dismiss
    - Lang-switch: sidebar altında TR/EN butonları
14. ✅ **Dark/light tema:**
    - `variables.css`: tüm renk token'ları CSS custom properties ile (mimari kararlar bölümündeki palette)
    - Varsayılan: dark mode (`--bg-primary: #000000`, `--accent-blue: #1D9BF0`)
    - `data-theme="light"` ile light mode override
    - JS toggle: `localStorage` + `theme` cookie (server-side render uyumu)
    - `prefers-color-scheme` medya sorgusu ile otomatik algılama
15. ✅ **Tüm template'lerde sabit metin yok** — her label, buton, başlık `{{ t }}` fonksiyonu ile

Manuel doğrulama:
- **TLS self-signed:** ilk başlatmada sertifika otomatik üretildi mi (`/var/lib/lankeeper/tls/`)
- `curl -k https://10.10.10.1:8443/login` → login sayfası dönüyor mu
- **TLS protokol:** `openssl s_client -connect 10.10.10.1:8443` → TLS 1.2+ kullanılıyor mu
- **mkcert:** mod değiştir → mkcert ile sertifika üret → CA indir → tarayıcıda uyarı yok mu
- **TLS settings:** sertifika durumu (expire tarihi, mod) doğru gösteriliyor mu
- Yanlış şifre → login sayfasında hata mesajı (HTMX swap), dile uygun mesaj
- Doğru şifre → dashboard'a redirect
- WAN IP'den erişim → 403
- **Dil testi:** `Accept-Language: en` ile istek → İngilizce UI
- **Dil değiştirme:** TR/EN butonlarına tıkla → sayfa seçilen dilde yenileniyor mu
- **Sidebar:** tüm navigasyon etiketleri aktif dile göre mi

### Phase 3: Network Interface + VLAN + PPPoE WAN + USB Tethering + IPv6 + Health Check (8 gün)
**Hedef:** Interface algılama ve isimlendirme, 802.1Q VLAN desteği (WAN + LAN), PPPoE ile internete bağlanma, auto-reconnect, ISP credential yakalama, interface health check + otomatik recovery.

Oluşturulacak dosyalar:
- `internal/services/network.go` — NIC algılama, interface label/role yönetimi
- `internal/services/pppoe.go` — pppd yönetimi + pppoe-server credential sniff
- `internal/services/healthcheck.go` — Interface internet checker + escalating recovery
- `configs/sysconf/pppoe-peer.tmpl`
- `configs/sysconf/pppoe-options.tmpl`
- `configs/sysconf/pppoe-server-options.tmpl` — credential yakalama config'i
- `internal/web/handlers/pppoe.go`
- `internal/web/handlers/network.go`
- `internal/web/handlers/healthcheck.go`
- `web/templates/pages/network.html`
- `web/templates/partials/interfaces.html` — Interface listesi + düzenleme
- `web/templates/partials/wan-status.html`
- `web/templates/partials/pppoe-sniff.html` — credential yakalama UI
- `web/templates/partials/healthcheck.html` — Health check durum kartları + config

Adımlar:
1. ✅ **Interface algılama ve yönetimi:**
   - `/sys/class/net/` tarayarak tüm fiziksel NIC'leri algıla (virtual, loopback hariç)
   - Her NIC için: device name, MAC, link state (up/down), speed, driver
   - İlk çalıştırmada algılanan NIC'leri `interfaces` config'e varsayılan değerlerle ekle
   - Web UI: algılanan interface listesi → her biri için label, role (wan/lan/unused), MTU düzenlenebilir
   - Label her yerde kullanılır: dashboard, firewall, QoS, PBR — ham device name yerine
   - Role değişikliği: uyarı + onay (WAN/LAN rolü değiştirmek ağ kesintisi yapar)
2. ✅ **802.1Q VLAN yönetimi:**
   - VLAN oluşturma: `ip link add link {parent_device} name {parent}.{vid} type vlan id {vid}`
   - VLAN silme: `ip link delete {parent}.{vid}`
   - VLAN IP atama: `ip addr add {address} dev {parent}.{vid}` (static) veya DHCP client
   - **WAN tarafı VLAN kullanım senaryoları:**
     - ISP IPTV trafik ayrımı (ör: VLAN 10 üzerinden IPTV, ana bağlantı tagged/untagged)
     - ISP'nin VoIP/data/IPTV'yi farklı VLAN'larda sunması (yaygın Türkiye senaryosu)
   - **LAN tarafı VLAN kullanım senaryoları:**
     - Misafir ağı: izole subnet, ana LAN'a erişim yok
     - IoT ağı: güvenilmeyen cihazlar için izole segment
     - Managed switch ile trunk port: router tek NIC üzerinden çoklu ağ segmenti
   - `isolated: true` flag → nftables'da inter-VLAN routing engellenir (misafir ↛ ana LAN)
   - VLAN bazlı DHCP: her izole VLAN için ayrı dnsmasq instance veya ayrı `dhcp-range` config
   - nftables entegrasyonu: VLAN interface'leri zone-based firewall'a dahil (input/forward chain)
   - Agent operations: `network.vlan.create`, `network.vlan.delete`, `network.vlan.up`, `network.vlan.down`
   - Startup restore: config'deki VLAN tanımlarını boot'ta oluştur
   - Web UI: VLAN listesi, ekleme formu (parent NIC dropdown, VID input, IP/subnet, isolated toggle), silme
3. ✅ `text/template` ile `/etc/ppp/peers/wan` ve options dosyası render
3. ✅ PPPoE service: Connect (`pppd call wan`), Disconnect (`kill pppd`), Status
4. ✅ Credentials `.credentials.enc`'den AES-256-GCM ile çözme
5. ✅ Auto-reconnect: pppd `persist` + `holdoff` seçenekleri
6. ✅ **IPv6 over PPPoE:**
   - pppd `+ipv6` seçeneği → IPv6CP negotiation etkinleştir
   - ISP IPv6 destekliyorsa ppp0 interface'de link-local IPv6 adresi oluşur
   - DHCPv6-PD client: `dhcpcd` veya `wide-dhcpv6-client` ile prefix delegation talep et
   - ISP'den /56 veya /64 prefix alınır → LAN interface'e atanır (SLAAC ile dağıtım)
   - `ipv6.enabled: auto` ise: IPv6CP başarılırsa otomatik etkinleşir, başarısızsa IPv4-only
   - Config'deki `ipv6.wan.prefixHint` ISP'den talep edilen prefix boyutunu belirler
   - PPPoE yeniden bağlandığında prefix değişebilir → LAN'a yeni RA gönderilir
   - System dependency: `wide-dhcpv6-client` paketi
7. ✅ Agent operations: `pppoe.connect`, `pppoe.disconnect`, `pppoe.status`
8. ✅ Network handler: interface listesi (label ile), WAN IP (IPv4 + IPv6), gateway, uptime
8. ✅ **PPPoE Credential Yakalama (pppoe-server):**
   - Agent op: `pppoe.sniff.start` → WAN NIC'te `pppoe-server` başlat (require-pap, debug, logfile)
   - ISP modem bağlandığında PAP username/password logdan parse
   - Agent op: `pppoe.sniff.stop` → pppoe-server durdur
   - Yakalanan credentials → AES-256-GCM ile `.credentials.enc`'ye kaydet
   - Web UI: "Credential Yakala" butonu → durum göstergesi → bulunan credentials
   - Güvenlik: credentials sadece maskelenmiş gösterilir (son 4 karakter), full gösterme yok
9. ✅ HTMX: interface kartları, bağlan/kes butonları → partial swap ile durum güncelleme
10. ✅ **Android USB Tethering (Yedek WAN):**
    - **Algılama:** udev rule ile Android telefon USB bağlandığında `usb0` (veya `rndis0`) interface otomatik tanınır
      - udev rule: `SUBSYSTEM=="net", ACTION=="add", ATTRS{idVendor}=="18d1", NAME="usb0"` (Google vendor ID)
      - Farklı telefon markaları: Samsung `04e8`, Xiaomi `2717` vb. → generic RNDIS class match: `DRIVER=="rndis_host"`
    - **DHCP client:** telefon USB tethering açıldığında `usb0` üzerinde DHCP server çalıştırır → router `dhclient usb0` ile IP alır
    - **NAT:** `table ip nat` → `oifname "usb0" masquerade` (PPPoE NAT'ın yanına)
    - **Failover mantığı (otomatik):**
      1. Health check PPPoE'yi fail olarak tespit eder
      2. Escalating actions sırasında `failoverUsb` aksiyonuna ulaşılır
      3. `usb0` interface aktif mi kontrol et (telefon bağlı + tethering açık)
      4. Aktifse: default route'u `usb0` üzerinden ayarla (`ip route replace default dev usb0 metric 100`)
      5. nftables'da USB interface için masquerade kuralı etkinleştir
      6. SSE ile web UI'a "USB tethering aktif" bildirimi gönder
    - **Failback mantığı (otomatik):**
      1. PPPoE bağlantısı geri geldiğinde (health check OK)
      2. `failbackDelay` süresi kadar bekle (stabil mi?)
      3. Default route'u tekrar `ppp0`'a çevir (`ip route replace default dev ppp0 metric 0`)
      4. USB masquerade kuralını devre dışı bırak
      5. SSE bildirimi: "PPPoE bağlantısı geri geldi"
    - **Manuel geçiş:** Web UI'dan "USB'ye Geç" / "PPPoE'ye Dön" butonları
    - **TTL Fix:** USB tethering aktifken ISP (mobil operatör) tethering tespiti → config'deki `usbTethering.ttlFix` etkinse TTL sabitleme
    - **Route metric:** PPPoE metric=0, USB metric=100 → PPPoE her zaman öncelikli
    - **Telefon algılama:** USB bağlantısı olmadığında interface yok → failover atlanır, sonraki aksiyona geçilir
    - Agent operations: `usb.activate`, `usb.deactivate`, `usb.status`
    - Web UI (network.html içinde section): USB tethering durumu (bağlı/bağlı değil, aktif WAN mı), enable/disable toggle, manuel geçiş butonları
11. ✅ **Health Check (Internet Connectivity Monitor):**
    - `healthcheck.go` service: goroutine ile periyodik kontrol (ping + HTTP)
    - Her check tanımı: interface, hedef listesi, interval, timeout, failure threshold/window
    - Kontrol mantığı: hedeflerden en az 1 başarılı → OK, hepsi başarısız → failure count++
    - Failure threshold aşılınca → escalating actions sırasıyla dene:
      1. `restartInterface` — interface down/up (`ip link set down/up`)
      2. `restartPppoe` — PPPoE bağlantısını yeniden kur (agent op: `pppoe.reconnect`)
      3. `failoverUsb` — USB tethering'e geç (telefon bağlıysa, agent op: `usb.activate`)
      4. `rebootSystem` — son çare, sistemi yeniden başlat (agent op: `system.reboot`)
    - Her action arasında configurable delay (önceki aksiyon sonucu beklenir)
    - Cooldown süresi: aksiyon sonrası tekrar failure saymaya başlamadan önce bekle
    - Agent operations: `healthcheck.restart_iface`, `healthcheck.restart_pppoe`
    - SSE: health check durum değişikliklerini real-time olarak web UI'a yayınla
    - Web UI (network.html içinde section): check listesi, her birinin durumu (OK/warning/critical), son kontrol zamanı, failure count, son aksiyon
    - Web UI config: check ekleme/düzenleme formu (hedefler, threshold'lar, aksiyonlar)
    - Manuel çalıştır butonu: tek check'i anında çalıştır ve sonucu göster
    - Reset butonu: failure counter'ı sıfırla (yanlış alarm sonrası)
    - Syslog'a bildirim: durum değişikliklerinde (OK→fail, fail→OK, aksiyon alındığında)
11. ✅ **i18n:** `{{ t .Lang "network.*" }}`, `{{ t .Lang "pppoe.*" }}` ve `{{ t .Lang "healthcheck.*" }}` ile tüm metinler

Manuel doğrulama:
- **Interface algılama:** tüm fiziksel NIC'ler listeleniyor mu
- **Label:** interface'e verilen isim dashboard ve diğer sayfalarda görünüyor mu
- **Role değişikliği:** WAN↔LAN swap sonrası ağ doğru çalışıyor mu
- `ppp0` interface ayağa kalkıyor mu
- İnternet erişimi: `ping 8.8.8.8`
- **IPv6 PPPoE:** `ip -6 addr show ppp0` → link-local adresi var mı (IPv6CP başarılı)
- **DHCPv6-PD:** ISP'den prefix alınıyor mu (`wide-dhcpv6-client` log)
- **IPv6 LAN:** LAN interface'de global IPv6 adresi atanmış mı (`ip -6 addr show`)
- **IPv6 auto:** ISP IPv6 desteklemiyorsa IPv4-only modda sorunsuz çalışıyor mu
- Auto-reconnect: pppd kill sonrası tekrar bağlanıyor mu
- Web UI'dan durum görünüyor + bağlan/kes çalışıyor mu (IPv4 + IPv6 adresleri)
- **Credential yakalama:** modem bağlanınca username/password yakalanıyor mu
- **Health check:** ping/HTTP kontrolleri periyodik çalışıyor mu
- **Failure escalation:** threshold aşılınca interface restart → pppoe restart → reboot sırası doğru mu
- **Cooldown:** aksiyon sonrası belirtilen süre boyunca tekrar aksiyon almıyor mu
- **Web UI:** check durumları gerçek zamanlı güncelleniyor mu, manuel çalıştır çalışıyor mu
- **Syslog:** durum değişiklikleri loglanıyor mu
- **VLAN oluşturma:** `ip link show` → VLAN interface görünüyor mu (ör: enp0s25.100)
- **VLAN WAN:** ISP IPTV VLAN'ından trafik alınabiliyor mu
- **VLAN LAN (misafir):** misafir ağı izole mi (misafir → ana LAN ping başarısız)
- **VLAN LAN (misafir):** misafir ağından internete çıkılabiliyor mu
- **VLAN DHCP:** misafir VLAN'da ayrı DHCP havuzundan IP alınıyor mu
- **VLAN boot:** reboot sonrası VLAN'lar otomatik oluşturulup ayağa kalkıyor mu
- **USB tethering algılama:** Android telefon USB ile bağlayınca `usb0` interface görünüyor mu (`ip link show usb0`)
- **USB tethering DHCP:** router `usb0` üzerinden IP alıyor mu (`dhclient usb0`)
- **USB failover:** PPPoE kesilince (kablo çek) → USB'ye otomatik geçiş oluyor mu
- **USB failback:** PPPoE geri gelince → otomatik ana bağlantıya dönüyor mu
- **USB manuel geçiş:** web UI'dan "USB'ye Geç" butonu çalışıyor mu
- **USB NAT:** USB tethering aktifken LAN cihazları internete çıkabiliyor mu
- **USB telefon yok:** telefon bağlı değilken failover USB'yi atlayıp sonraki aksiyona geçiyor mu
- TR/EN dillerinde tüm network/PPPoE/VLAN/USB metinleri doğru mu

### Phase 4: nftables Firewall + NAT + IPv6 (5 gün)
**Hedef:** Zone-based firewall, NAT masquerade (IPv4), IPv6 stateful firewall (NAT66 yok), dual-stack MSS clamping, port forwarding, watchdog rollback.

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
1. ✅ nftables Go `text/template` şablonu:
   - `table inet filter` — input/forward/output chains (dual-stack: IPv4 + IPv6 tek tabloda)
   - `table ip nat` — prerouting (DNAT) + postrouting (masquerade) — **yalnızca IPv4, NAT66 yok**
   - MSS clamping (IPv4): `tcp flags syn tcp option maxseg size set rt mtu`
   - MSS clamping (IPv6): `ip6 nexthdr tcp tcp flags syn tcp option maxseg size set rt mtu` (IPv6 header 40 byte → MSS 1432)
   - Connection tracking: `ct state established,related accept`
   - WAN input: default drop, established + ICMP (IPv4) + ICMPv6 (IPv6)
   - **ICMPv6 zorunlu allowlist (RFC 4890):**
     - NDP: `nd-router-solicit` (133), `nd-router-advert` (134), `nd-neighbor-solicit` (135), `nd-neighbor-advert` (136)
     - MLD: `mld-listener-query` (130), `mld-listener-report` (131), `mld2-listener-report` (143)
     - Error: `destination-unreachable` (1), `packet-too-big` (2), `time-exceeded` (3), `parameter-problem` (4)
     - Ping: `echo-request` (128), `echo-reply` (129)
   - LAN→WAN forward: accept + masquerade (IPv4), accept (IPv6 — NAT yok, global prefix ile doğrudan çıkış)
   - Rate limiting: brute force koruması
2. ✅ AtomicChange: snapshot → validate (`nft -c -f`) → apply → watchdog
3. ✅ Watchdog: 30s goroutine timer, onay gelmezse rollback
4. ✅ Port forwarding: DNAT + forward kuralı CRUD
5. ✅ sysctl: `net.ipv4.ip_forward=1`, `net.ipv6.conf.all.forwarding=1` (IPv6 etkinse, config'e bağlı)
6. ✅ **TTL Fix (tethering bypass):**
   - nftables postrouting chain'de: `ip ttl set {value}` (varsayılan 64)
   - Tüm WAN'a çıkan paketlerde TTL sabitlenir → ISP router arkasındaki cihazları ayırt edemez
   - Web UI: toggle switch (aç/kapat) + TTL değeri input (varsayılan 64, 1-255 arası)
   - Config: `firewall.ttlFix.enabled` + `firewall.ttlFix.value`
   - nftables şablonunda conditional render: enabled ise kural eklenir, değilse eklenmez
   - Değişiklik anında uygulanır (AtomicChange + watchdog ile)
7. ✅ HTMX: kural ekleme formu, silme, watchdog onay banner'ı, TTL Fix toggle
8. ✅ **i18n:** Tüm template metinleri `{{ t .Lang "firewall.*" }}` ile — kural tipleri, watchdog uyarısı, onay butonu, TTL Fix açıklaması

Manuel doğrulama:
- NAT çalışıyor mu (LAN → internet)
- WAN → LAN engelli mi
- Port forwarding çalışıyor mu
- Watchdog: onaylanmayan değişiklik 30s sonra rollback oluyor mu
- `nft list ruleset` beklenen kuralları gösteriyor mu
- **TTL Fix:** etkinken `traceroute` veya `tcpdump` ile WAN çıkışında TTL sabit mi
- **TTL Fix kapalı:** TTL normal davranıyor mu (her hop'ta azalıyor)
- **IPv6 forward:** LAN cihazı IPv6 global adresle internete çıkabiliyor mu (`ping6 2001:4860:4860::8888`)
- **IPv6 ICMPv6:** NDP çalışıyor mu (neighbor discovery), RA alınıyor mu (`rdisc6 eth0`)
- **IPv6 firewall:** WAN'dan gelen bağlantılar engelleniyor mu (IPv4 ile aynı politika)
- **IPv6 MSS clamping:** PPPoE üzerinden büyük IPv6 paketler sorunsuz geçiyor mu
- **IPv6 NAT yok:** LAN cihazlarında global prefix adresi var mı (`ip -6 addr show`)
- TR/EN dillerinde firewall metinleri doğru mu

### Phase 5: Unbound DNS + dnsmasq DHCP + Query Logging + IPv6 RA (5 gün)
**Hedef:** Recursive DNS resolver + reklam engelleme (Unbound), DHCP sunucu (dnsmasq), DNS query logging + istatistikler, config dosyası yönetimi.

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
- `web/templates/partials/dns-querylog.html` — Sorgu geçmişi tablosu (filtreli, paginated)
- `web/templates/partials/lease_table.html`

Adımlar:
1. ✅ **Unbound config template:**
   - `server:` — interface, access-control, cache-size, verbosity
   - IPv6 dinleme: `interface: ::0` (dual-stack), `do-ip6: yes`
   - AAAA sorgu desteği: hem IPv4 hem IPv6 upstream'lere sorgulama
   - Recursive mode: `root-hints` dosyası ile
   - Blocklist: `include: /etc/unbound/blocklist.conf` (her satır: `local-zone: "domain" always_refuse`)
   - Opsiyonel DNS-over-TLS upstream: `forward-zone:` → `forward-tls-upstream: yes`
2. ✅ **Blocklist yönetimi:**
   - StevenBlack/hosts formatını indir (`net/http`)
   - Parse: `0.0.0.0 domain` → `local-zone: "domain" always_refuse`
   - Atomic write → `unbound-control reload`
   - Zamanlanmış güncelleme (goroutine ticker)
3. ✅ **dnsmasq config template:**
   - `port=0` (DNS kapalı, sadece DHCP)
   - `dhcp-range=10.10.10.100,10.10.10.250,12h`
   - `dhcp-option=option:router,10.10.10.1`
   - `dhcp-option=option:dns-server,10.10.10.1` (Unbound'a yönlendir)
   - Statik lease'ler: `dhcp-host=aa:bb:cc:dd:ee:ff,10.10.10.10,desktop`
   - **IPv6 SLAAC/RA (dnsmasq):**
     - `enable-ra` — Router Advertisement gönderimi etkinleştir
     - `dhcp-range=::,constructor:lan,ra-only,64,12h` — SLAAC modu (stateless, adres RA ile dağıtılır)
     - `dhcp-option=option6:dns-server,[::1]` — IPv6 DNS sunucu (Unbound)
     - `ra-param=lan,{raInterval},0,0` — RA gönderim aralığı config'den
     - RDNSS option: RA ile DNS bilgisi (RFC 8106)
     - ULA prefix aktifse: ULA adresleri de RA ile dağıtılır (global + ULA dual)
     - IPv6 desteği kapalıysa (`ipv6.enabled: off`) bu satırlar config'e eklenmez
4. ✅ **Lease parse:** `/var/lib/misc/dnsmasq.leases` dosyasını oku → `{expiry, mac, ip, hostname}`
5. ✅ **DNS istatistikleri:** `unbound-control stats_noreset` → cache hits, misses, query count
6. ✅ **DNS Query Logging:**
   - Unbound config: `log-queries: yes`, `verbosity: 2`, `logfile:` → `/var/log/unbound/queries.log`
   - Log formatı: `[timestamp] unbound: info: 10.10.10.15 google.com. A IN` şeklinde satır bazlı
   - Go'da log dosyasını tail-parse eden goroutine:
     - Her satırı parse et: timestamp, client IP, domain, query type (A/AAAA/CNAME/...), durum (NOERROR/REFUSED/NXDOMAIN)
     - DHCP lease ile eşleştir: IP → hostname/MAC (hangi cihaz sorgulamış)
     - Engellenen sorgular: `REFUSED` → blocklist tarafından engellendi olarak işaretle
   - In-memory ring buffer: son N sorgu (configurable, varsayılan 10.000)
   - Periyodik aggregation (her 5dk):
     - Top clients: en çok sorgu yapan cihazlar
     - Top domains: en çok sorgulanan domainler
     - Top blocked: en çok engellenen domainler
     - Saatlik/günlük sorgu grafiği verisi
   - Logrotate entegrasyonu: `maxSize` + `retention` config'den, `/etc/logrotate.d/unbound-querylog`
   - Web UI: toggle ile aç/kapat (Unbound reload gerekir), log temizleme butonu
   - Web UI tablo: son sorgular (domain, cihaz, tür, durum, zaman), filtreleme (cihaz, domain arama, sadece engellenenler), pagination
   - Web UI: top clients/domains/blocked kartları (HTMX poll ile güncellenen)
   - Performans: büyük log dosyalarında `io.Scanner` ile satır bazlı okuma, tam dosyayı belleğe yükleme yok
7. ✅ **Cihaz listesi:** lease'lerden MAC+IP+hostname (VPN modülü kullanacak)
8. ✅ **Config değişikliği akışı:** Go template render → atomic write → agent `SIGHUP` gönder
9. ✅ **Agent operations:** `dns.reload` (unbound-control reload), `dhcp.reload` (SIGHUP dnsmasq), `dns.querylog.clear` (log dosyasını truncate)
10. ✅ HTMX: lease tablosu, DNS istatistikleri, blocklist durumu, query log tablosu
11. ✅ **i18n:** Tüm template metinleri `{{ t .Lang "dns.*" }}` ve `{{ t .Lang "dhcp.*" }}` ile

Manuel doğrulama:
- `dig @10.10.10.1 google.com` → Unbound recursive çözümleme çalışıyor mu
- `dig @10.10.10.1 ads.example.com` → blocklist engelleme çalışıyor mu (REFUSED)
- DHCP: yeni cihaz IP alıyor mu, lease tablosunda görünüyor mu
- Statik lease ekle/sil çalışıyor mu
- `unbound-control stats_noreset` → istatistikler web UI'da doğru mu
- **Query log:** DNS sorgusu yap → query log tablosunda görünüyor mu
- **Query log filtre:** cihaz bazlı filtre çalışıyor mu, domain arama çalışıyor mu
- **Engellenen sorgular:** blocklist'teki domain sorgulandığında "engellendi" olarak işaretleniyor mu
- **Top listeler:** en çok sorgulanan domain, en aktif cihaz, en çok engellenen domain doğru mu
- **Toggle:** query logging kapatılınca log durur mu, açılınca tekrar başlar mı
- **Log temizleme:** clear butonu log dosyasını temizliyor mu
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
1. ✅ Monitor service: goroutine, 1s interval — CPU, RAM, temp, throughput
   - `/proc/stat` (CPU), `/proc/meminfo` (RAM), `/sys/class/thermal` (temp)
   - `/proc/net/dev` (interface byte counters → throughput hesaplama)
2. ✅ SSE broker: channel-based pub/sub, goroutine per client
3. ✅ SSE endpoint: `GET /events/stats` → `text/event-stream`
4. ✅ **Dashboard stat kartları (X design system):**
   - `.card` bileşeni: `--bg-surface` zemin, `--border-color` kenar, 16px radius
   - Her kart: ikon + etiket (`--text-secondary`) + değer (`--text-primary`, 23px, bold)
   - Durum renkleri: bağlı → `--accent-green`, kopuk → `--accent-red`, uyarı → `--accent-yellow`
   - Kartlar CSS Grid: `grid-template-columns: repeat(auto-fit, minmax(200px, 1fr))`
5. ✅ Canvas grafik: bandwidth history (son 60 veri noktası, 1s interval), `--accent-blue` çizgi rengi
6. ✅ **Responsive layout:**
   - Desktop: 3-sütun grid (sidebar 275px + content + panel 350px)
   - Tablet (< 1024px): sidebar daraltılır (ikon-only, 68px)
   - Mobil (< 768px): sidebar → bottom tab bar, panel gizlenir, tek sütun
7. ✅ Settings sayfası: hostname, timezone, password değiştir, tema toggle (dark/light)
8. ✅ **i18n:** Dashboard stat etiketleri, birim formatları `{{ t .Lang "dashboard.*" }}` ile

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
1. ✅ CAKE qdisc:
   - Egress: `tc qdisc add dev ppp0 root cake bandwidth {upload}kbit`
   - Ingress: IFB device → `tc qdisc add dev ifb0 root cake bandwidth {download}kbit wash ingress`
2. ✅ Congestion control: `sysctl net.ipv4.tcp_congestion_control={bbr|cubic}`
3. ✅ BBR prerequisite: `sysctl net.core.default_qdisc=fq`
4. ✅ Profiller: cake (varsayılan), fq_codel, none
5. ✅ Agent ops: `qos.apply`, `qos.clear`
6. ✅ HTMX: profil seçimi (radio), bandwidth input, apply butonu
7. ✅ **i18n:** QoS profil açıklamaları, etiketler, birimler `{{ t .Lang "qos.*" }}` ile

Manuel doğrulama:
- `tc -s qdisc show dev ppp0` → CAKE aktif mi
- Bufferbloat testi (flent rrul veya waveform.com/tools/bufferbloat)
- BBR/CUBIC geçişi çalışıyor mu
- TR/EN dillerinde QoS sayfası metinleri doğru mu

### Phase 8: WireGuard + OpenVPN + Policy-Based Routing (11 gün)
**Hedef:** WireGuard client + server, OpenVPN client + server (PKI), tam kapsamlı PBR motoru, web UI ile yönetim.

Oluşturulacak dosyalar:
- `internal/services/vpn.go` — WireGuard client + server yönetimi
- `internal/services/openvpn.go` — OpenVPN client + server + PKI yönetimi
- `internal/services/routing.go` — PBR motoru (kural eşleştirme, nftables entegrasyonu, DNS-based routing)
- `configs/sysconf/wireguard-client.conf.tmpl` — WG client config template
- `configs/sysconf/wireguard-server.conf.tmpl` — WG server config template
- `configs/sysconf/openvpn-server.conf.tmpl` — OpenVPN server config template
- `configs/sysconf/openvpn-client.conf.tmpl` — OpenVPN client config template
- `configs/defaults/vpn.yaml`
- `configs/defaults/routing.yaml`
- `internal/web/handlers/vpn.go`
- `internal/web/handlers/openvpn.go`
- `internal/web/handlers/routing.go`
- `web/templates/pages/vpn.html`
- `web/templates/pages/openvpn.html`
- `web/templates/pages/routing.html`
- `web/templates/partials/vpn_clients.html`
- `web/templates/partials/vpn_server.html`
- `web/templates/partials/vpn_peer_form.html`
- `web/templates/partials/ovpn_clients.html`
- `web/templates/partials/ovpn_server.html`
- `web/templates/partials/ovpn_client_form.html`
- `web/templates/partials/policy_list.html`
- `web/templates/partials/policy_form.html`
- `web/templates/partials/policy_status.html`
- `web/static/js/htmx-sortable.js`

Adımlar:
1. ✅ **WireGuard client tünel yönetimi (outbound):**
   - Config template: key, endpoint, allowed IPs, DNS
   - IPv6 desteği: `AllowedIPs = 0.0.0.0/0, ::/0` (full tunnel dual-stack)
   - Tünel CRUD: `wg-quick up/down wgN`
   - Keypair: `exec.Command("wg", "genkey")` + `wg pubkey`
   - Per-tünel routing table: `ip route add default dev wgN table {table_id}` + `ip -6 route add default dev wgN table {table_id}`
2. ✅ **WireGuard server (inbound — road warrior):**
   - İlk kurulumda otomatik server keypair üretimi (`wg genkey` + `wg pubkey`)
   - Server config template: `[Interface]` (listenPort, privateKey, address) + `[Peer]` blokları
   - Server interface: `wg0-server` (client interface'lerden ayrı namespace: `wg0`, `wg1`... client, `wgs0` server)
   - Server subnet: `10.10.11.0/24` + `fd10:10::0/64` ULA (LAN'dan ayrı, configurable)
   - nftables entegrasyonu:
     - Server peer'lardan LAN'a erişim: `iif wgs0 oif {lan_iface} accept` (forward chain)
     - Server peer'lardan internete çıkış: `iif wgs0 oif ppp0 accept` + NAT masquerade
     - Opsiyonel: peer bazında LAN erişim kısıtlaması (sadece belirli IP/subnet)
   - Peer yönetimi:
     - Peer ekleme: `wg set wgs0 peer {pubkey} allowed-ips {ip}/32 preshared-key {psk}`
     - Peer silme: `wg set wgs0 peer {pubkey} remove`
     - PresharedKey: opsiyonel ama önerilen (quantum-resistance)
     - Keepalive: peer bazında configurable (NAT traversal)
     - IP havuzu: server subnet içinden otomatik atama (10.10.11.2, .3, .4...)
   - **Client config dosyası oluşturma (indirilebilir):**
     - Peer eklenince Go tarafında client `.conf` dosyası render:
       ```ini
       [Interface]
       PrivateKey = {peer_private_key}
       Address = 10.10.11.2/32, fd10:10::2/128
       DNS = 10.10.10.1
       MTU = 1420

       [Peer]
       PublicKey = {server_public_key}
       PresharedKey = {psk}
       Endpoint = {router_wan_ip_or_ddns}:{port}
       AllowedIPs = 0.0.0.0/0, ::/0    # Full tunnel (dual-stack)
       # AllowedIPs = 10.10.10.0/24, fd00:abcd:1234::/48  # Split tunnel (sadece LAN)
       ```
     - İki mod: full tunnel (tüm trafik router üzerinden) veya split tunnel (sadece LAN'a erişim)
     - İndirme: `GET /vpn/server/peer/{name}/config` → `.conf` dosyası
   - **QR kodu (mobil cihazlar için):** UYGULANDI, ama tarayıcı tarafında
     - Go'da üretim REDDEDİLDİ: `go-qrcode` yedinci bir modül olurdu, `exec.Command("qrencode")` ise private key'i root agent'a argüman olarak verir ve `ps` çıktısında görünür kılardı
     - `web/static/js/qrcode.js` elle yazılmış ISO/IEC 18004 encoder; config aynı indirme endpoint'inden `fetch` ile alınır ve `<canvas>` üzerine çizilir (`<img>`/base64 DEĞİL, HTML sink'inden kaçınmak için)
     - WireGuard mobil app: QR kodu okut → tek tıkla bağlan. OpenVPN profilleri gömülü sertifikalarla QR kapasitesini aştığı için modal "çok büyük" der ve indirmeye yönlendirir
   - **Endpoint adresi:**
     - Router'ın WAN IP'si: ppp0 interface'den oku
     - DDNS desteği: configurable hostname (ör: `ev.example.com`)
     - Port forwarding notu: ISP modem bridge modda değilse 51820 port forwarding gerekir
   - Agent operations: `vpn.server.up`, `vpn.server.down`, `vpn.server.reload`
3. ✅ **OpenVPN client (outbound):**
   - `.ovpn` dosya import: web UI'dan dosya yükle → config parse + validate → kaydet
   - `openvpn --config {file} --daemon` ile bağlantı başlat
   - Auth-user-pass desteği: username/password `.credentials.enc`'de saklanır
   - Durum izleme: `openvpn --management` socket veya log parse → connected/disconnected/error
   - PBR entegrasyonu: OpenVPN client da fwmark + routing table ile policy routing'e dahil
   - Agent operations: `openvpn.client.connect`, `openvpn.client.disconnect`
4. ✅ **OpenVPN server (inbound):**
   - **PKI altyapısı (easy-rsa wrapper):**
     - `easyrsa init-pki` → `/etc/openvpn/pki/` dizini oluştur
     - `easyrsa build-ca nopass` → CA sertifikası
     - `easyrsa gen-req server nopass` + `easyrsa sign-req server server` → server sertifikası
     - `easyrsa gen-dh` → Diffie-Hellman parametreleri
     - `openvpn --genkey secret ta.key` → tls-auth HMAC anahtarı
     - Tüm bu adımlar web UI'dan tek tıkla ("PKI Oluştur" butonu)
   - **Client sertifika yönetimi:**
     - `easyrsa gen-req {name} nopass` + `easyrsa sign-req client {name}` → client cert+key
     - Revoke: `easyrsa revoke {name}` + `easyrsa gen-crl` → CRL güncelle
     - Enable/disable: revoke yerine CCD (client-config-dir) ile `disable` flag
   - **Server config template:**
     ```ini
     port 1194
     proto udp
     dev tun
     ca /etc/openvpn/pki/ca.crt
     cert /etc/openvpn/pki/issued/server.crt
     key /etc/openvpn/pki/private/server.key
     dh /etc/openvpn/pki/dh.pem
     tls-auth /etc/openvpn/pki/ta.key 0
     server 10.10.12.0 255.255.255.0
     server-ipv6 fd20:20::/64               # IPv6 dual-stack VPN subnet
     push "redirect-gateway def1 ipv6"      # Full tunnel (dual-stack)
     push "dhcp-option DNS 10.10.10.1"
     cipher AES-256-GCM
     auth SHA256
     keepalive 10 120
     persist-key
     persist-tun
     crl-verify /etc/openvpn/pki/crl.pem
     ```
   - **Client .ovpn dosyası oluşturma (inline sertifikalar):**
     - Tüm sertifikalar (ca, cert, key, tls-auth) tek .ovpn dosyasına inline embed
     - İndirme: `GET /openvpn/server/client/{name}/config`
     - QR kodu: config string → qrencode → mobil OpenVPN app
   - nftables: `iif tun0 oif {lan_iface} accept`, masquerade
   - Agent operations: `openvpn.server.start`, `openvpn.server.stop`, `openvpn.server.reload`
5. ✅ **PBR motoru — kural eşleştirme:**
   - `routing.yaml`'dan politika kurallarını yükle
   - Her politikayı nftables kuralına çevir:
     - Kaynak eşleştirme: `ip saddr {device_ip}` veya `ether saddr {mac}`
     - Hedef IP/CIDR: `ip daddr {cidr}`
     - Port/protokol: `tcp dport {port}` veya `udp dport {port-range}`
     - Zaman: nftables `meta hour` + `meta day` (kernel 5.4+)
   - fwmark atama: `meta mark set {fwmark}`
   - `ip rule add fwmark {mark} lookup {table_id} priority {prio}`
   - `ct mark` ile reply paketlerde fwmark korunması
4. ✅ **Domain-based routing:**
   - Politikadaki domain listesi → Unbound'a `local-zone` + `local-data` hook
   - DNS yanıtından çözümlenen IP'leri yakala (unbound-control dump_cache parse)
   - nftables named set: `nft add element inet filter pbr_{policy_name} { resolved_ip }`
   - Kural: `ip daddr @pbr_{policy_name} meta mark set {fwmark}`
   - Goroutine: TTL bazlı set temizleme + yeni sorgu ile refresh
5. ✅ **nftables PBR chain:**
   - `chain pbr_policies` — priority sırasıyla kural zinciri
   - Firewall template güncelleme: PBR chain'i forward chain'e entegre
8. ✅ **Kill switch:** VPN client tünel down (WG veya OVPN) → ilgili politikadaki cihazların trafiğini engelle
9. ✅ **Startup restore:** `routing.yaml` + `vpn.yaml` + `openvpn` config'den tüm tünel + server + politika kurallarını kur
10. ✅ **Web UI — WireGuard sayfası (HTMX):**
    - İki tab/section: **WG Client Tünelleri** + **WG Server**
    - Client: tünel listesi (durum, handshake, transfer), CRUD formu
    - Server: açma/kapama toggle, dinleme portu, subnet, peer listesi
    - Peer kartı: isim, IP, son handshake, transfer, durum (online/offline)
    - Peer ekleme formu: isim gir → keypair + psk + IP otomatik üret → config indir/QR göster
    - Config indirme butonu (`.conf` dosyası) + QR kodu görüntüleme (mobil için)
    - Tunnel mode seçimi: full tunnel / split tunnel (peer bazında)
11. ✅ **Web UI — OpenVPN sayfası (HTMX):**
    - İki tab/section: **OVPN Client** + **OVPN Server**
    - Client tab: .ovpn dosya import (file upload), bağlantı listesi, connect/disconnect butonları
    - Server tab:
      - PKI durumu: CA oluşturulmuş mu, server cert geçerlilik süresi
      - "PKI Oluştur" butonu (ilk kurulum, tek seferlik)
      - Server açma/kapama toggle + ayarlar formu (port, protocol, cipher)
      - Client sertifika listesi: isim, oluşturma tarihi, durum (aktif/revoked), son bağlantı
      - Client ekleme: isim gir → sertifika oluştur → .ovpn dosya indir / QR göster
      - Client revoke/enable/disable butonları
      - Bağlı client listesi (real-time): IP, bağlantı süresi, transfer
12. ✅ **Web UI — PBR sayfası (HTMX):**
    - Politika listesi: sürükle-bırak ile priority sıralama (`htmx-sortable.js`)
    - Politika ekleme/düzenleme formu:
      - Kaynak: cihaz dropdown (DHCP lease'lerden) veya CIDR input
      - Hedef: IP/CIDR input veya domain listesi (textarea, wildcard destekli)
      - Port/protokol: input + TCP/UDP/any seçimi
      - Zaman: schedule picker (başlangıç-bitiş saat + gün seçimi)
      - Action: dropdown (wan, WG tünel, OVPN tünel, drop)
    - Enable/disable toggle
    - Canlı eşleşme durumu: SSE ile hangi cihaz hangi politikaya eşleşiyor
13. ✅ **i18n:** `{{ t .Lang "vpn.*" }}`, `{{ t .Lang "openvpn.*" }}` ve `{{ t .Lang "routing.*" }}` ile tüm UI metinleri

Manuel doğrulama:
- **WG Client:** `wg show wg0` → client tünel aktif mi, handshake var mı
- **WG Server:** `wg show wgs0` → server dinliyor mu, peer listesi doğru mu
- **WG Road warrior:** telefondan QR okut → WireGuard app ile bağlan → LAN erişimi
- **WG Config indirme:** `.conf` dosyasını laptop'ta import et → bağlantı kurulabiliyor mu
- **WG Full vs split tunnel:** full tunnel'da tüm trafik router üzerinden mi
- **WG Firewall:** VPN server peer'ları LAN'a erişebiliyor mu, internet çıkışı çalışıyor mu
- **OVPN Client:** .ovpn dosya import → connect → `ip a show tun1` aktif mi
- **OVPN Server PKI:** "PKI Oluştur" → CA + server cert oluşturuldu mu
- **OVPN Client cert:** client sertifika oluştur → .ovpn dosya indir → bağlantı kurulabiliyor mu
- **OVPN Revoke:** client revoke → bağlantı reddediliyor mu
- **OVPN PBR:** OpenVPN client da PBR politikasına dahil edilebiliyor mu
- **Kaynak bazlı PBR:** Xbox'a politika ata → VPN'den çıkıyor mu, diğer cihazlar direkt mi
- **Hedef IP bazlı:** 1.2.3.0/24'e giden trafik → belirtilen tünelden çıkıyor mu
- **Domain bazlı:** netflix.com politikası → `dig netflix.com`, çözümlenen IP VPN'den mi
- **Port bazlı:** UDP 3478 → direkt, geri kalan → VPN
- **Zaman bazlı:** schedule aktifken VPN, schedule dışında direkt
- **Kombinasyon:** Xbox + UDP gaming portları → direkt, Xbox geri kalan → VPN
- **Priority:** yüksek öncelikli kural düşük öncelikliden önce uygulanıyor mu
- **Kill switch:** client tünel down → ilgili cihaz internetsiz mi
- **Sürükle-bırak:** politika sıralaması değiştirince priority güncelleniyor mu
- TR/EN dillerinde VPN ve routing sayfası metinleri doğru mu

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
1. ✅ Samba config template: global + per-share
2. ✅ Paylaşım CRUD: oluştur/güncelle/sil → `smb.conf` regenerate → `smbcontrol reload-config`
3. ✅ M3U parser:
   - `net/http` ile M3U/M3U8 indir
   - `#EXTINF` parse: grup, başlık, URL
   - İçerikleri gruplara göre klasörlere indir (goroutine pool)
   - Kodi `.strm` dosyaları oluştur
4. ✅ Zamanlanmış sync: `time.Ticker` goroutine
5. ✅ HTMX: paylaşım listesi, M3U sync butonu, durum göstergesi
6. ✅ **i18n:** Paylaşım form etiketleri, M3U durum mesajları `{{ t .Lang "nas.*" }}` ile

Manuel doğrulama:
- Samba erişimi: Windows/macOS/Linux'tan bağlanabiliyor mu
- M3U parse: `.strm` dosyaları doğru klasör yapısında mı
- Kodi'den medya oynatılabiliyor mu
- TR/EN dillerinde NAS sayfası metinleri doğru mu

### Phase 10: Storage + Syslog + NTP + Backup + Hardening (5 gün)
**Hedef:** RAID izleme, disk sağlığı, syslog sunucu/client, NTP sunucu/client, config backup, güvenlik sertleştirme.

Oluşturulacak dosyalar:
- `internal/services/storage.go`
- `internal/services/backup.go`
- `internal/services/syslog.go` — rsyslog config render + reload
- `internal/services/ntp.go` — chrony config render + reload + durum okuma
- `configs/sysconf/rsyslog.conf.tmpl` — rsyslog sunucu/client config şablonu
- `configs/sysconf/chrony.conf.tmpl` — chrony NTP sunucu/client config şablonu
- `internal/web/handlers/storage.go`
- `internal/web/handlers/system.go`
- `internal/web/handlers/syslog.go`
- `internal/web/handlers/ntp.go`
- `web/templates/pages/storage.html`
- `web/templates/pages/syslog.html`
- `web/templates/pages/ntp.html`
- `web/templates/pages/settings.html`
- `web/templates/partials/raid_status.html`
- `web/templates/partials/syslog-logs.html`
- `web/templates/partials/syslog-sources.html`
- `web/templates/partials/ntp_status.html`
- `deploy/factory-reset.sh`
- `deploy/backup.sh`

Adımlar:
1. ✅ RAID: `mdadm --detail` parse, degraded alarm + disk keşfi (`lsblk`) + RAID oluşturma (`mdadm --create`) + format+mount web UI'dan
2. ✅ SMART: `smartctl -a` → sağlık skoru, sıcaklık, hata
3. ✅ Config backup: `tar.gz` export/import (config/ + unbound + dnsmasq + chrony config)
4. ✅ Factory reset: varsayılan config restore
5. ✅ Güvenlik sertleştirme:
   - systemd: ProtectSystem=strict, PrivateTmp, NoNewPrivileges
   - sysctl: rp_filter, tcp_syncookies, icmp_ignore_bogus
   - SSH: key-only, LAN-only
   - CSP header, X-Frame-Options, X-Content-Type-Options
6. ✅ HDD spin-up stagger: `hdparm -S`
7. ✅ **Syslog sunucu:**
   - rsyslog config template: `module(load="imudp")` + `input(type="imudp" port="514")`
   - Per-host log dizini: `/var/log/remote/{hostname}/`
   - Log rotation: rsyslog `outchannel` veya logrotate config
   - Web UI: uzak cihaz loglarını filtreli görüntüleme (host, facility, severity)
   - Opsiyonel TLS: `module(load="imtcp") input(type="imtcp" port="6514" ... streamdriver.mode="1")`
8. ✅ **Syslog client:**
   - rsyslog forward kuralı: `*.* @@remote:514` (TCP) veya `*.* @remote:514` (UDP)
   - Facility seçimi: config'den hangi logların iletileceğini belirle
   - Opsiyonel TLS forwarding
9. ✅ **NTP sunucu (chrony):**
   - chrony config template render:
     - Client modu: `server 0.tr.pool.ntp.org iburst` (upstream NTP kaynakları)
     - Server modu: `allow 10.10.10.0/24` + `allow 10.10.11.0/24` (LAN + VPN peer'ları)
     - `local stratum 10` — upstream'ler ulaşılamaz olsa bile LAN'a zaman servisi ver
     - `rtcsync` — sistem saatini RTC'ye yaz
     - `makestep 1.0 3` — ilk senkronizasyonda büyük fark varsa anında düzelt
   - `chronyc sources` parse: upstream kaynak durumu, offset, jitter
   - `chronyc tracking` parse: son senkronizasyon, drift, stratum
   - Agent ops: `ntp.reload` (systemctl reload chronyd), `ntp.force_sync` (chronyc makestep)
   - nftables entegrasyonu: UDP 123 sadece LAN + VPN subnet'ten kabul (input chain)
   - DHCP entegrasyonu: dnsmasq config'e `dhcp-option=option:ntp-server,10.10.10.1` ekle
     → LAN cihazları DHCP ile otomatik olarak router'ı NTP sunucu olarak alır
   - Web UI: senkronizasyon durumu (offset, stratum, kaynak listesi), upstream değiştirme, force sync butonu
10. ✅ Agent ops: `syslog.reload` (systemctl reload rsyslog)
11. ✅ **i18n:** Storage, syslog, NTP, settings, backup sayfaları `{{ t .Lang "storage.*" }}`, `{{ t .Lang "syslog.*" }}`, `{{ t .Lang "ntp.*" }}`, `{{ t .Lang "settings.*" }}` ile
12. ✅ **i18n doğrulama:** Tüm locale JSON dosyalarında eksik anahtar testi (build time check)

Manuel doğrulama:
- RAID durumu doğru gösteriliyor mu
- Config export → factory reset → import → çalışıyor mu
- Güvenlik header'ları mevcut mu (`curl -I`)
- **Syslog sunucu:** başka cihazdan `logger -n 10.10.10.1 "test"` → log görünüyor mu
- **Syslog client:** router logları harici sunucuya iletiliyor mu
- **Syslog Web UI:** host filtresi, severity filtresi, pagination çalışıyor mu
- **NTP sunucu:** LAN cihazından `ntpdate -q 10.10.10.1` → zaman sorgulanabiliyor mu
- **NTP client:** `chronyc tracking` → upstream'e senkronize mi, offset düşük mü
- **NTP DHCP:** LAN cihazı DHCP ile NTP sunucu adresi alıyor mu (`dhclient -v`)
- **NTP Web UI:** kaynak listesi, offset, stratum doğru gösteriliyor mu, force sync çalışıyor mu
- **NTP firewall:** WAN'dan UDP 123'e erişim engellenmiş mi
- TR/EN dillerinde storage, syslog, NTP ve settings sayfaları doğru mu
- **i18n bütünlük:** `tr.json` ve `en.json` aynı anahtarlara sahip mi (eksik anahtar yok)

### Phase 11: Deployment — install.sh + Debian Preseed ISO (3 gün)
**Hedef:** Sıfır dokunuş kurulum: bootable USB → RAID-1 disk kurulumu → tüm paketler + Go binary → ilk boot'ta router hazır.

Oluşturulacak dosyalar:
- `deploy/install.sh` — Tam kapsamlı kurulum scripti (Phase 1'de iskelet, burada tamamlanır)
- `deploy/iso/build-iso.sh` — Debian preseed ISO oluşturma scripti
- `deploy/iso/preseed.cfg` — Debian unattended install preseed dosyası
- `deploy/iso/post-install.sh` — Preseed sonrası kurulum (install.sh'ın non-interactive versiyonu)
- `deploy/iso/grub.cfg` — UEFI + legacy BIOS dual-boot GRUB config

Adımlar:

1. ✅ **`install.sh` finalize (interactive mode):**
   - Phase 1'de oluşturulan iskelet scripti tamamla
   - Tüm Phase 1-10 bileşenlerinin kurulumu dahil
   - İnteraktif mod: admin şifresi, interface seçimi, WAN tipi (PPPoE/DHCP) soruları
   - Idempotent: tekrar çalıştırılabilir (mevcut config'i bozmaz, `--force` ile override)
   - `--check` modu: kurulumu doğrula, eksikleri raporla

2. ✅ **Debian Preseed dosyası (`preseed.cfg`):**
   - Debian 12 Bookworm netinst ISO üzerine preseed
   - Dil: Türkçe, timezone: Europe/Istanbul, keyboard: trq
   - **Yarı katılımsız (semi-attended):** sadece disk seçimi interaktif, geri kalan otomatik
   - **`early_command` ile disk seçim ekranı:**
     - `lsblk` ile tüm fiziksel diskleri algıla (ad, boyut, model)
     - 1 disk varsa otomatik seç, 2+ disk varsa `whiptail` radiolist göster
     - Kullanıcı OS diskini seçer → `debconf-set partman-auto/disk` + `grub-installer/bootdev`
     - Kalan diskler dokunulmaz — NAS için web UI'dan yapılandırılır
   - **Disk bölümleme:** tek disk, `atomic` recipe (boot + swap + root)
   - Paket seçimi: `standard`, `ssh-server` (minimal, GUI yok)
   - Root hesabı devre dışı, `lankeeper` kullanıcısı oluştur
   - `late_command`: `post-install.sh`'ı chroot içinde çalıştır

3. ✅ **Post-install scripti (`post-install.sh`):**
   - `install.sh`'ın non-interactive versiyonu (tüm cevaplar preseed'den)
   - Tüm apt paketlerini kur (nftables, wireguard-tools, unbound, dnsmasq, chrony, samba...)
   - Go binary'yi ISO'dan `/usr/local/bin/` altına kopyala
   - systemd unit dosyalarını yerleştir + enable
   - udev rules (NIC MAC-based naming, USB RNDIS)
   - Varsayılan config dosyalarını yerleştir
   - sysctl parametreleri
   - İlk TLS sertifikası (self-signed)
   - İlk boot'ta web UI setup wizard için flag oluştur (`/var/lib/lankeeper/.first-boot`)
   - GRUB: RAID-1 aware, degraded boot allowed

4. ✅ **ISO build scripti (`build-iso.sh`):**
   - Girdi: resmi Debian 12 netinst ISO + Go binary (cross-compile edilmiş)
   - Çıktı: `lankeeper-installer.iso` (custom preseed + binary gömülü)
   - İşlem:
     - Debian ISO'yu aç (`xorriso` veya `bsdtar`)
     - `preseed.cfg` + `post-install.sh` + Go binary'yi ISO'ya ekle
     - GRUB config güncelle: `auto=true preseed/file=/cdrom/preseed.cfg`
     - ISO'yu yeniden oluştur (`xorriso -as mkisofs`)
     - Opsiyonel: USB yazılabilir hybrid ISO (`isohybrid`)
   - Makefile entegrasyonu: `make iso` → cross-compile + ISO build
   - CI/CD: GitHub Actions'da ISO build (release artifact olarak)

5. ✅ **İlk Boot Setup Wizard:**
   - `/var/lib/lankeeper/.first-boot` dosyası varsa web UI'da setup wizard göster
   - **İlk boot'ta bridge-based erişim:**
     - `.first-boot` flag'i aktifken TÜM fiziksel NIC'ler `br0` bridge'e eklenir
     - Bridge'e tek IP atanır: `10.10.10.1/24` — subnet çakışması yok, tüm portlar aynı LAN
     - Kullanıcı herhangi bir porta kablo takıp `https://10.10.10.1:8443` adresinden web UI'a erişir
     - Wizard'da kullanıcı NIC'leri görür (MAC, driver, speed) ve WAN rolü atadığı NIC'i seçer
     - WAN atanan NIC bridge'den çıkarılır (`ip link set dev X nomaster`), PPPoE/DHCP'ye geçer
     - Kalan NIC'ler LAN olarak bridge'de kalır (çoklu LAN portu)
     - Wizard tamamlandığında bridge kaldırılabilir veya LAN bridge olarak kalabilir
     - `.first-boot` silinir → bundan sonra sadece LAN portundan web UI erişimi
   - Wizard adımları:
     1. Admin şifresi belirleme
     2. Interface rol seçimi: algılanan NIC'ler listelenir (MAC, driver, speed bilgisiyle), kullanıcı WAN ve LAN rollerini atar
     3. WAN yapılandırma: PPPoE credentials veya DHCP client
     4. LAN subnet: varsayılan `10.10.10.0/24` veya özel
     5. Temel DNS ayarları (recursive veya forwarder)
   - Wizard tamamlandığında `.first-boot` silinir, normal dashboard gösterilir
   - Wizard atlanabilir (ileri düzey kullanıcı doğrudan config düzenler)

6. ✅ **Makefile hedefleri:**
   - `make build` — Go binary derle (Linux amd64)
   - `make install` — `install.sh` çalıştır (lokal kurulum)
   - `make iso` — Preseed ISO oluştur (cross-compile + ISO build)
   - `make release` — Binary + ISO'yu tar.gz olarak paketle

Manuel doğrulama:
- **install.sh:** temiz Debian 12 minimal'e çalıştır → web UI erişilebilir mi
- **install.sh idempotent:** ikinci çalıştırma mevcut config'i bozmuyor mu
- **install.sh --check:** eksik paket/config doğru raporlanıyor mu
- **Preseed ISO:** USB'den boot → tamamen otomomatik kurulum tamamlanıyor mu
- **RAID-1:** `cat /proc/mdstat` → md0 active raid1, her iki disk üyesi
- **RAID-1 degraded boot:** tek disk çıkar → sistem boot oluyor mu
- **İlk boot:** web UI setup wizard gösteriliyor mu
- **Setup wizard:** admin şifresi + PPPoE credentials gir → internet bağlantısı kuruluyor mu
- **make iso:** GitHub Actions'da ISO build başarılı mı
- TR/EN dillerinde setup wizard metinleri doğru mu

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

| Risk                                      | Mitigation                                                                        |
|-------------------------------------------|-----------------------------------------------------------------------------------|
| PMTU black-holing (PPPoE MTU 1492)        | Phase 4'te MSS clamping zorunlu                                                   |
| NIC isimlendirme değişimi (reboot)        | udev rules by MAC address (`setup-interfaces.sh`)                                 |
| VPN policy route'lar reboot'ta kaybolur   | Agent startup'ta `vpn.yaml`'dan restore                                           |
| Firewall kuralı hatalı → ağ kilitlenir    | AtomicChange + 30s watchdog rollback                                              |
| PicoPSU 180W, 6 disk ile surge riski      | HDD spin-up stagger (`hdparm -S`)                                                 |
| Web UI XSS                                | `html/template` auto-escaping + CSP header + agent op whitelist                   |
| PPPoE credential sızıntısı                | AES-256-GCM encryption at rest, memory-only decrypt                               |
| Unbound/dnsmasq crash → DNS/DHCP çalışmaz | systemd restart policy + Go health check + degraded mode UI uyarısı               |
| Single point of failure (tek cihaz)       | Config backup + factory reset + RAID-1 depolama                                   |
| Go binary update sırasında downtime       | systemd: `ExecStartPre` ile binary swap, graceful shutdown                        |
| HTMX: full page refresh gerekebilir       | `hx-boost` ile link'leri HTMX'e çevir, minimal JS fallback                        |
| Health check reboot döngüsü               | Cooldown süresi + max reboot count/24h limiti + reboot sonrası grace period       |
| VPN server private key sızması            | AES-256-GCM at rest, peer config indirmede one-time token, QR timeout             |
| VPN server WAN IP değişimi (PPPoE)        | DDNS desteği (configurable hostname), ip-up script ile DDNS güncelleme            |
| DNS query log disk dolması                | logrotate (maxSize + retention), ring buffer in-memory, toggle ile kapatılabilir  |
| OpenVPN PKI private key sızması           | CA/server key /etc/openvpn/pki/ (700 perms), backup'ta AES-256-GCM encrypt        |
| OpenVPN DH parametresi üretimi yavaş      | `easyrsa gen-dh` arka planda, UI'da ilerleme göstergesi, ~2-5dk (i5 3470)         |
| ISP IPv6 desteği yok/kısıtlı              | `ipv6.enabled: auto` → IPv6CP başarısızsa IPv4-only, ULA ile LAN içi IPv6 korunur |
| DHCPv6-PD prefix değişimi (PPPoE)         | PPPoE reconnect sonrası yeni prefix → LAN'a RA ile dağıtım, geçiş süresi ~30s     |
| ICMPv6 engellenmesi → IPv6 çalışmaz       | RFC 4890 zorunlu allowlist (NDP, MLD, error messages) — asla drop edilmez         |
| IPv6 privacy extension tracking           | RA'da privacy extension önerisi (RFC 4941), temporary addresses                   |
| Self-signed cert tarayıcı uyarısı         | mkcert modu ile LAN'da güvenilir CA, ACME ile public domain desteği               |
| ACME DNS challenge API key sızması        | Token `.credentials.enc`'de AES-256-GCM, agent op whitelist                       |
| Let's Encrypt rate limit (5/hafta)        | Staging ortamı ile test, production'da dikkatli kullanım                          |
| USB tethering telefon bağlı değil         | failoverUsb aksiyonu telefon yoksa atlanır, sonraki aksiyona geçilir              |
| Mobil operatör tethering algılama         | USB üzerinden TTL Fix (ayrı toggle), hotspot tespiti bypass                       |
| USB tethering bant genişliği düşük        | Yedek amaçlı — sadece temel bağlantı, QoS/VPN devre dışı bırakılabilir            |
| USB interface ismi değişkenliği           | udev rule RNDIS class match (vendor-agnostic), Samsung/Xiaomi/Google test         |
| Preseed RAID-1 disk sırası değişir        | Preseed'de disk serial/ID ile eşleştirme, `/dev/disk/by-id/` kullanımı            |
| ISO build reproducibility                 | Makefile + pinned Debian ISO checksum + Go binary hash                            |
| UEFI Secure Boot + RAID-1                 | Preseed'de EFI partition her iki diskte, `grub-install` her iki diske             |

## Tahmini Toplam Süre

| Phase | Konu                                                   | Gün | Kümülatif |
|-------|--------------------------------------------------------|-----|-----------|
| 1     | İskelet + Agent IPC                                    | 3   | 3         |
| 2     | Web + Auth + HTMX Layout                               | 3   | 6         |
| 3     | Network + VLAN + PPPoE + USB Tethering + IPv6 + Health | 8   | 14        |
| 4     | nftables Firewall + NAT + IPv6                         | 5   | 19        |
| 5     | Unbound DNS + DHCP + Query Logging + IPv6 RA           | 5   | 24        |
| 6     | Dashboard + SSE                                        | 3   | 27        |
| 7     | SQM/QoS                                                | 3   | 30        |
| 8     | WireGuard + OpenVPN + PBR                              | 11  | 41        |
| 9     | Samba NAS + M3U                                        | 3   | 44        |
| 10    | Storage + Syslog + NTP + Backup                        | 5   | 49        |
| 11    | Deployment — install.sh + Preseed ISO                  | 3   | 52        |

**Toplam: ~52 geliştirme günü** (tek geliştirici, her gün 4-6 saat efektif çalışma varsayımı)

---

## Roadmap — Post-v0.1.0

v0.1.0 (2026-05-06) ile yukarıdaki 11 faz tamamlandı. v0.2.0 ile v0.5.0 arasında planlanan başlıkların tamamı gerçeklendi; hepsi aşağıdaki "Tamamlananlar" bölümündedir.

### Sonraki adaylar (önceliksiz)

Aşağıdakiler ya hiç başlamadı ya da yarım kaldı. Sıralama öncelik belirtmez.

**Planlanmış ama uygulanMAmış işler:**

- **Kaynak düzenleme (PUT) route'ları.** Plan çoğu liste için ekle/düzenle/sil öngörüyordu; uygulama ekle + sil ile yetindi. Arayüzler istisna: `POST /network/interface` aynı id ile gönderildiğinde günceller. Firewall kuralları ve açık portlar dışında toggle yok, PBR politikaları düzenlenemiyor, yalnızca silinip yeniden eklenebiliyor.
- **WireGuard client tüneli CRUD.** `/vpn/client/{name}/connect` ve `/disconnect` var; tünel ekleme ve silme yok. Outbound tüneller yalnızca config dosyasından tanımlanıyor.
- **`/events/healthcheck` ve `/events/routing` SSE kanalları.** Yalnızca `/events/stats` ve `/events/qos` uygulandı.

**Yeni fikirler:**

- DoH SERVER (Unbound 1.20+ ile client'lar 443'te LANKeeper'a DoH yapsın). TLS sertifika yönetimi mevcut `EnsureTLSCert` ile uyumlu; ayrı feature olarak takip.
- Grafana dashboard JSON paketi (mevcut `/metrics` üzerinde, sample scrape config README'de hazır).

**Bakım borcu:**

- `.claude/skills/version-update/` hâlâ `CHANGELOG.md` üzerinden çalışıyor (Step 4 dosyayı günceller, Step 5 stage'ler, Step 9 release notlarını awk ile çıkarır) ama `CHANGELOG.md` depodan silindi. Skill şu anda çalışmaz; ya onarılmalı ya changelog geri getirilmeli.
- `ci.yml` ve `buildsys/workflow_pins_test.go` içindeki `actions/*` tag muafiyetinin gerekçesi Dependabot'a dayanıyor, fakat `.github/dependabot.yml` kaldırıldı. Tag'ler artık elle bump ediliyor; gerekçe metni bunu yansıtmıyor.
- `dns.go` ve `monitor.go` içindeki iki `bufio.Scanner` döngüsü `scanner.Err()` kontrol etmiyor. golangci-lint yakalamıyor, yalnızca gopls işaretliyor.
- **Satır içi event handler'lar CSP ile çelişiyor.** Sunucu `script-src 'self'` gönderiyor, `'unsafe-inline'` YOK. 11 şablonda 35 `onclick`/`onchange` özniteliği ve birkaç `hx-on` var; hiçbiri tarayıcıda çalışmaz. "Peer ekle", "İstemci ekle" gibi form açma butonları bu yüzden ölü. Ya hepsi delegasyonlu dinleyiciye taşınmalı ya da politika gevşetilmeli; ikincisi XSS yüzeyini geri açar. QR tetikleyicileri bilinçli olarak `data-qr-url` ile yazıldı ve `buildsys/qr_assets_test.go` bunu koruyor.
- **`qrencode` paketi artık kullanılmıyor.** Hem `deploy/install.sh` hem `deploy/iso/build-iso.sh` listelerinde duruyor, fakat QR üretimi tarayıcı tarafında yapılıyor ve hiçbir Go kodu bu binary'yi çağırmıyor. İki listeden de çıkarılabilir.
- **ACME canlı yolu doğrulanMAdı.** Cloudflare istek şekli, zone arama, manual challenge sözleşmesi ve yenileme kararı testlerle kapsanıyor; gerçek bir CA'ya karşı uçtan uca akış (Let's Encrypt staging + public domain) bu ortamda çalıştırılamadı.

### Tamamlananlar

**Boşluk kapatma turu — arayüzden erişilemeyen özellikler**

- Health check durum partial'ı route'a bağlandı (`GET /network/healthcheck/status`, 10 saniyede bir tazelenir) ve `network.html` içindeki ikinci, reset butonsuz kopya kaldırıldı. Aynı turda `partials/healthcheck.html` `.HealthChecks` yerine `.Data.HealthChecks` okuyacak şekilde düzeltildi: sayfa yolunda `.Data` bir map olduğu için eksik anahtar sessizce boş dönüyordu, `RenderPartial` yolunda ise aynı arama `*PageData` üzerinde eksik alan, yani execute hatası oluyordu.
- TTL fix, USB tethering (enable/disable/activate/deactivate/auto-failover) ve arayüz düzenleme route'ları eklendi. Arayüz silme son LAN arayüzünü reddeder; USB activate telefon bağlı değilken 400 döner.
- First-boot köprüsü `serve.go` içinde devreye alındı. Hata ölümcül değil: köprü bir erişilebilirlik kolaylığı, onun yüzünden açılışı reddetmek DNS, DHCP ve firewall'ı da düşürürdü. `Complete` bayrağı siler ama köprüyü ayakta bırakır, çünkü operatörün bağlı olduğu adresi o taşır; kaldırma ayrı ve açık bir kontrol.
- WireGuard peer private key'leri `enc:v1:` AES-256-GCM ile saklanmaya başladı ve config yeniden indirilebilir oldu (`GET /vpn/server/peer/{name}/config`). Şifreleme sırasında peer slice'ı derin kopyalanır; yüzeysel kopya çalışan process'e ciphertext bırakırdı. Key'i olmayan (alan eklenmeden önce yazılmış) peer'lar çalışmaya devam eder, yalnızca yeniden indirme kaybolur ve UI bunu rozetle söyler.
- QR kodu üretimi tarayıcıda: `web/static/js/qrcode.js` elle yazılmış bir ISO/IEC 18004 encoder (byte mode, sürüm 1-40, EC L/M). Go tarafında yapmak ya yedinci bir modül eklerdi ya da private key'i root agent'a komut argümanı olarak verirdi, orada `ps` çıktısında görünürdü. Config mevcut indirme endpoint'inden `fetch` ile alınır, `<canvas>` üzerine çizilir, `innerHTML` kullanılmaz. Kodword akışı bağımsız bir referans uygulamayla 372 yük üzerinde, fonksiyon desenleri ve version bilgisi 40 sürümün tamamında karşılaştırıldı; çözme testi 372 sentetik yükte ve gerçek WireGuard config'lerinde (UTF-8 dahil) geçti.
- TLS üç modu da UI'dan yönetilebilir: self-signed yeniden üretme, mkcert (CA indirme dahil) ve ACME DNS-01 (Cloudflare + manual, varsayılan staging, 30 gün kala otomatik yenileme). Her mod geçişi aynı sırayı izler: üret, doğrula, modu yaz, yeniden başlat.

**v0.2.0 — IPv6 Operator Visibility**

- DHCPv6 Prefix Delegation UI — `internal/services/ipv6.go`, `configs/sysconf/dhcp6c.conf.tmpl` ve `dhcp6c-script.tmpl`, `internal/web/handlers/ipv6.go`, `web/templates/pages/ipv6.html`. `wide-dhcpv6` `dhcp6c` ayrı bir systemd unit'i olarak çalışır (`lankeeper-dhcp6c.service`, `Conflicts=wide-dhcpv6-client.service`). Lease JSON'u `/var/lib/lankeeper/state/ipv6-prefix.json` altına yazılır; fsnotify ile parent dizin izlenir (atomik mv Create+Rename+Chmod ürettiği için 150ms debounce) ve prefix değişimi `SetOnLeaseChange` üzerinden firewall'ı otomatik Apply + Confirm eder, 30 saniyelik watchdog atlanır. RA drop-in'i (`/etc/dnsmasq.d/lankeeper-ipv6-ra.conf`) yalnızca `IPv6Service` sahiplenir; yükü RDNSS + DNSSL + MTU + ULA fallback'tir. Renew/release butonları, subnet map ve prefix geçmişi UI'da.
- 6in4 Tunneling — `internal/services/sixinfour.go`. HE.net tunnelbroker: `ip tunnel add <dev> mode sit`, MTU 1480 (PPPoE altında 1452). Tunnel /64'ü point-to-point, LAN'a dağıtılan prefix ayrı Routed /64 veya /48. DDNS `https://ipv4.tunnelbroker.net/nic/update?hostname=<ID>` Basic Auth ile, good/nochg/badauth/abuse cevapları ayrıştırılır; WAN IPv4 değişiminde on-connect zincirinden tetiklenir. Firewall IPv4 WAN'da `ip protocol 41 accept` açar, IPv6 için MASQUERADE uygulanmaz. DHCPv6-PD ile mutually exclusive (mod geçişinde eski düzlem `ApplyConfig` ÖNCE söker, yeni düzlem SONRA başlar).

**v0.3.x-v0.5.0 — Gözlemlenebilirlik, şifreli DNS, yedekleme, S2S**

- Prometheus `/metrics` endpoint — LAN-only (mevcut LANOnly middleware), no auth, exposition format 0.0.4. Stdlib-only writer (~50 LOC `fmt.Fprintf`), client_golang dep yok. ~30 metric family: build/uptime/CPU/RAM/temp/iface bytes/DHCP/DNS/per-client bandwidth (64 cap, SHA1[:4] MAC hash)/WG peers/S2S/OpenVPN/backup/PPPoE/IPv6/firewall. Composer nil-safe: bir alt sistem ölü olsa scrape'in tamamı düşmez.
- DNS-over-HTTPS upstream — `/dns` sayfasında "DNS Şifreleme Modu" kartı: Plain / DoT / DoH radyosuyla seçim. Unbound DoH upstream'i HİÇBİR sürümde desteklemediği için (NLnetLabs/unbound#525 hâlâ açık), `dnscrypt-proxy` Debian paketi 127.0.0.1:5353'te localhost-only dinler ve Unbound `forward-zone "."` ile oraya yönlendirir. 10 hazır sağlayıcı (Cloudflare/Quad9/Google/AdGuard/Mullvad) + `https://host/dns-query` URL veya `sdns://` stamp özel girişi. SSRF guard, port allowlist, char allowlist; native `dnsmessage` ile probe (5s outer timeout). DoT ile mutex. Uygulama sırası yöne bağlıdır: AÇARKEN önce dnscrypt-proxy sonra Unbound reload, KAPATIRKEN önce Unbound reload sonra dnscrypt-proxy stop.
- Backup snapshot scheduling — `/backup` sayfası ile cron-bazlı otomatik şifreli dışa aktarım. Local + S3-uyumlu (SigV4 native, aws-sdk-go yok) + SFTP (`pkg/sftp`) hedefleri, per-target retention, 50 girişlik history ring buffer. Cron parser `@hourly`/`@daily`/`@weekly`/`@monthly`/`@yearly` aliases + 5 alan destekli (`*`, n, n-m, n,m,p, `*/k`); range+step (`1-10/2`) uygulanMAdı. Vixie DOM/DOW semantiği. Hedef başına atomic write (tmp + rename local/sftp; tek PUT s3). AES-256-GCM scrypt pipeline'ına bağlı; boş passphrase form alanı stored değeri korur. SFTP hedefi `hostKeyFingerprint` pinlenene kadar bağlanmayı reddeder ve red mesajı sunucunun sunduğu parmak izini adlandırır.
- Per-client (per-MAC) bandwidth grafiği — `lankeeper_qos` nftables tablosuyla per-MAC counter çiftleri (forward chain priority -200), `/events/qos` SSE kanalı, `/qos` sayfasında canlı tablo + sparkline. CAKE class stats yerine nftables forward sayaçları kullanıldı (CAKE per-host stats netlink-only; pretty-print yok). Counter id'si `SHA1(normalize edilmiş MAC)[:4]`, tavan 64 istemci.
- WireGuard site-to-site sihirbazı — iki LANKeeper arasında HMAC-imzalı invite + ack token alışverişiyle tek-tıklık peer kurulumu. Token zarfı Version ve Kind ("invite"/"ack") taşır, böylece rol replay'i engellenir; iki fazlı state machine, idempotent CancelInvite ve 5 dakikalık GC ticker. İmzalama anahtarı `/var/lib/lankeeper/credentials/s2s-token.key` altında, UI'dan döndürülebilir. `wg syncconf` ile canlı reload. Plain `wg`/pfSense ile birlikte çalışırlık manuel paste-config ile korunuyor.
- OTA update — `UpdateService` GitHub Releases API'sinden `runtime.GOARCH`'a uygun tar.gz varlığını indirir, binary'yi atomik değiştirir, 60 saniyelik watchdog ile geri alır. Kalıcı update state'i `/var/lib/lankeeper/update-state.json` içinde restart'ı atlatır. GRUB boot branding'ini yeni sürümle günceller. `/api/version` auth'suz endpoint. Her release'e iki `linux-*.tar.gz` arşivi ve tam olarak `SHA256SUMS` adlı bir varlık eklenmelidir; aksi halde kurulu her router güncellemeyi görür ama uygulayamaz.
- Preseed installer ISO + arm64 — `make iso` / `make iso-all` Docker içinde offline unattended kurulum imajı üretir; paket bağımlılıkları `dist/packages/{arch}/` altında build'ler arası cache'lenir. Hem amd64 hem arm64 hedeflenir. ISO builder root olarak çalışıp `xorriso`/`fdisk`/`dd` kullandığı için depo kökü ASLA mount edilmez: `configs/` ve `deploy/` read-only, yalnızca `dist/` yazılabilir girer.
- Güvenlik sertleştirme — agent komut çözümlemesi `trustedBinDirs` üzerinden yeniden yapılır (doğrulanan değer ile çalıştırılan değer aynı string), UDS `root:<grup>` 0660 + `SO_PEERCRED` peer UID kontrolü, agent 16 eşzamanlı bağlantı sınırı, SSE broker başına 32 stream + 30s keep-alive, tüm outbound HTTP `safefetch.go` içindeki SSRF korumalı client'lardan geçer, CSP'ye `base-uri` ve `form-action` eklendi, CSRF token'ı auth sınırlarında döndürülür, `gosec` ve `govulncheck` CI gate'i oldu, üçüncü taraf GitHub Action'ları commit SHA'ya pinlendi.
