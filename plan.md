# Home Router Software — Implementation Plan (Go + HTMX)

## Context

Turkcell Superonline'ın ISP modemleri bufferbloat sorununa neden oluyor ve 1 Gbps bağlantıda SQM/QoS desteği sunmuyor. Mevcut ZTE modem yerine Intel i5 3470 tabanlı özel donanım üzerine sıfırdan router yazılımı geliştirilecek. Hedef: PPPoE WAN bağlantısı, nftables firewall, WireGuard VPN, Samba NAS ve web dashboard'u tek bir Go binary'sinde birleştirmek.

## Kurallar

1. **Her değişiklikte commit atılır.** Fonksiyonel bir birim tamamlandığında hemen commit.
2. **Asla yama yapılmaz.** Sorunun kök nedeni bulunur ve oradan çözülür.
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

- Wi-Fi yönetimi (kullanıcı ayrı AP'ler kullanıyor)
- Harici DNS/DHCP web UI (Pi-hole, AdGuard Home) — Unbound + dnsmasq doğrudan Go'dan yönetilecek
- Veritabanı (tüm config YAML dosyalarında)
- JavaScript framework (React/Vue/Svelte yok — HTMX + server-side rendering)
- Çoklu ISP / load balancing (tek PPPoE ana bağlantı + USB tethering yedek)
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
|---------------|-------|---------|-------------------|
| Sayfa başlığı | 23px  | 700     | 28px              |
| Bölüm başlığı | 20px | 700     | 24px              |
| Gövde metin   | 15px  | 400     | 20px              |
| Gövde kalın   | 15px  | 700     | 20px              |
| Alt metin      | 13px | 400     | 16px              |
| Küçük          | 12px | 400     | 16px              |

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
- 10 yıl geçerlilik, `/var/lib/home-router/tls/` altında saklanır
- Yenileme: web UI'dan tek tıkla yeni cert üret

**mkcert (LAN kullanımı):**
- `mkcert` komutu ile lokal CA oluşturulur ve sertifika imzalanır
- CA sertifikası LAN cihazlarına yüklenirse tarayıcı uyarısı olmaz
- Web UI: "CA Sertifikası İndir" butonu → cihazlara import
- Agent op: `tls.mkcert.generate` → `mkcert -install` + `mkcert {hostnames}`
- System dependency: `mkcert` (Go binary, apt veya direct download)

**Let's Encrypt (ACME):**
- LAN-only router'da HTTP-01 challenge çalışmaz → DNS-01 challenge zorunlu
- Desteklenen DNS provider'lar: Cloudflare, Route53, manual (TXT record)
- Go ACME client: `golang.org/x/crypto/acme/autocert` veya `lego` kütüphanesi
- Public domain gerekli (ör: `router.example.com`)
- Otomatik yenileme: cert expire'a 30 gün kala goroutine ile renew
- DNS API token'ı `.credentials.enc`'de saklanır

**Ortak:**
- Tüm modlarda HSTS header gönderilmez (LAN-only, IP erişimi bozulur)
- TLS 1.2+ zorunlu, TLS 1.0/1.1 kapalı
- Cipher suite: Go'nun varsayılan güvenli seti (ECDHE + AES-GCM)
- Sertifika değişikliği anında uygulanır (graceful restart)

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
│   │   ├── tls.go               # TLS sertifika yönetimi (self-signed, ACME, mkcert)
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
│   │       ├── vpn.go            # WireGuard tünel yönetimi
│   │       ├── openvpn.go       # OpenVPN server/client handler'ları
│   │       ├── routing.go       # Policy-based routing kuralları (CRUD + sıralama)
│   │       ├── nas.go            # Samba paylaşımları
│   │       ├── storage.go        # RAID durumu, disk sağlığı
│   │       ├── healthcheck.go     # Health check durum/config handler'ları
│   │       ├── syslog.go          # Syslog sunucu/client yapılandırma
│   │       ├── ntp.go            # NTP sunucu/client yapılandırma + durum
│   │       └── system.go         # Ayarlar, yedekleme, reboot
│   ├── services/
│   │   ├── pppoe.go              # pppd yönetimi
│   │   ├── firewall.go           # nftables ruleset oluşturma + uygulama
│   │   ├── dns.go                # Unbound config yönetimi + blocklist + unbound-control
│   │   ├── dhcp.go               # dnsmasq config yönetimi + lease parse
│   │   ├── qos.go                # tc + CAKE qdisc yönetimi
│   │   ├── vpn.go                # WireGuard tunnel yönetimi
│   │   ├── openvpn.go            # OpenVPN server + client yönetimi + PKI
│   │   ├── routing.go            # Policy-based routing motoru (PBR)
│   │   ├── nas.go                # Samba config + M3U parser
│   │   ├── storage.go            # mdadm + smartctl
│   │   ├── monitor.go            # Sistem istatistikleri toplayıcı (goroutine)
│   │   ├── healthcheck.go       # Interface internet checker + otomatik recovery
│   │   ├── usbtethering.go     # Android USB tethering failover yönetimi
│   │   ├── syslog.go             # rsyslog config yönetimi (sunucu + client)
│   │   ├── ntp.go                # chrony config yönetimi (sunucu + client)
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
│   │   │   ├── openvpn.html      # OpenVPN server + client yönetimi
│   │   │   ├── routing.html      # Policy-based routing yönetimi
│   │   │   ├── dns.html
│   │   │   ├── dhcp.html
│   │   │   ├── qos.html
│   │   │   ├── nas.html
│   │   │   ├── storage.html
│   │   │   ├── syslog.html
│   │   │   ├── ntp.html
│   │   │   ├── settings.html
│   │   │   └── login.html
│   │   └── partials/
│   │       ├── sidebar.html      # Sidebar navigasyon
│   │       ├── stats_card.html   # Dashboard stat kartı (SSE ile güncellenir)
│   │       ├── bandwidth.html    # Bandwidth grafiği container
│   │       ├── lease_table.html  # DHCP lease tablosu (HTMX swap)
│   │       ├── dns_querylog.html # DNS sorgu geçmişi (filtre + pagination)
│   │       ├── fw_rules.html     # Firewall kural listesi
│   │       ├── vpn_clients.html   # WG client tünel listesi + durum
│   │       ├── vpn_server.html   # WG server durumu + peer listesi + QR
│   │       ├── vpn_peer_form.html# WG peer ekleme/düzenleme formu
│   │       ├── ovpn_clients.html # OpenVPN client listesi + durum
│   │       ├── ovpn_server.html  # OpenVPN server durumu + client listesi
│   │       ├── ovpn_client_form.html # OpenVPN client sertifika oluşturma
│   │       ├── vpn_panel.html    # VPN cihaz atama paneli (PBR entegrasyonu)
│   │       ├── vpn_device.html   # Tekil cihaz kartı (draggable)
│   │       ├── policy_list.html  # PBR politika listesi (sürükle-bırak sıralama)
│   │       ├── policy_form.html  # PBR politika ekleme/düzenleme formu
│   │       ├── policy_status.html# PBR canlı eşleşme durumu
│   │       ├── share_list.html   # Samba paylaşım listesi
│   │       ├── raid_status.html  # RAID durumu
│   │       ├── healthcheck.html  # Health check durum kartları + config formu
│   │       ├── ntp_status.html   # NTP senkronizasyon durumu + kaynak listesi
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
│   │   ├── rsyslog.conf.tmpl    # rsyslog sunucu/client config
│   │   ├── chrony.conf.tmpl     # chrony NTP sunucu/client config
│   │   ├── wireguard.conf.tmpl  # WireGuard interface config
│   │   ├── openvpn-server.conf.tmpl # OpenVPN server config
│   │   ├── openvpn-client.conf.tmpl # OpenVPN client config
│   │   └── smb.conf.tmpl         # Samba paylaşım config
│   └── defaults/
│       ├── router.yaml           # Varsayılan ana config
│       ├── firewall.yaml         # Varsayılan firewall kuralları
│       ├── qos.yaml              # Varsayılan QoS profilleri
│       ├── vpn.yaml              # Boş VPN config
│       ├── routing.yaml          # Varsayılan PBR politikaları (boş)
│       └── nas.yaml              # Boş NAS config
├── deploy/
│   ├── systemd/
│   │   ├── home-router.target    # Orchestration target
│   │   ├── home-router-agent.service
│   │   └── home-router-web.service
│   ├── install.sh                # Tam kurulum scripti (Debian 12 üzerine)
│   ├── setup-interfaces.sh       # udev kuralları + NIC isimlendirme
│   ├── factory-reset.sh          # Fabrika ayarlarına dönüş
│   ├── backup.sh                 # Cron backup scripti
│   └── iso/
│       ├── build-iso.sh          # Debian preseed ISO oluşturma scripti
│       ├── preseed.cfg           # Debian unattended install preseed dosyası
│       ├── post-install.sh       # Preseed sonrası çalışan kurulum scripti
│       └── grub.cfg              # UEFI/BIOS dual-boot GRUB config
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
  hostname: "home-router"
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
      cn: "home-router.lan"              # Common Name
      validDays: 3650                    # Geçerlilik süresi (10 yıl)
      sans:                              # Subject Alternative Names
        - "home-router.lan"
        - "10.10.10.1"
        - "router.local"
    acme:                                  # Let's Encrypt (ACME)
      enabled: false
      email: ""                          # ACME hesap e-posta
      domain: ""                         # Public domain (ör: router.example.com)
      provider: "letsencrypt"            # letsencrypt | letsencrypt-staging
      dnsChallenge:                      # DNS-01 challenge (LAN-only router için HTTP-01 çalışmaz)
        provider: ""                     # cloudflare | route53 | manual
        apiToken: ""                     # .credentials.enc'den
    mkcert:
      caInstalled: false                 # mkcert CA'sı cihazlara yüklenmiş mi (bilgilendirme)

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
  mode: "dhcpv6-pd"                        # dhcpv6-pd | static | slaac — WAN tarafı IPv6 alma yöntemi
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
| Method | Path                       | Tür     | Açıklama                               |
|--------|----------------------------|---------|-----------------------------------------|
| GET    | /network                   | Sayfa   | Ağ ayarları + interface yönetimi        |
| GET    | /partials/interfaces       | Partial | Algılanan tüm NIC'ler + durumları       |
| PUT    | /network/interface/{id}    | Partial | Interface label, role, MTU düzenle      |
| GET    | /partials/vlans            | Partial | VLAN listesi + durumları                |
| POST   | /network/vlan              | Partial | Yeni VLAN ekle                          |
| PUT    | /network/vlan/{id}         | Partial | VLAN düzenle                            |
| DELETE | /network/vlan/{id}         | Partial | VLAN sil                                |
| GET    | /partials/wan-status       | Partial | WAN durum kartı                        |
| POST   | /pppoe/connect             | Partial | PPPoE bağlantısını başlat               |
| POST   | /pppoe/disconnect          | Partial | PPPoE bağlantısını kes                  |
| PUT    | /pppoe/config              | Partial | PPPoE ayarlarını güncelle               |
| POST   | /pppoe/sniff               | Partial | PPPoE credential yakalama başlat        |
| GET    | /partials/pppoe-sniff      | Partial | Yakalama durumu + bulunan credentials   |
| POST   | /pppoe/sniff/stop          | Partial | Yakalama işlemini durdur                |
| GET    | /partials/usb-tethering    | Partial | USB tethering durumu (bağlı/bağlı değil)|
| POST   | /network/usb-tethering/enable  | Partial | USB tethering failover'ı etkinleştir|
| POST   | /network/usb-tethering/disable | Partial | USB tethering failover'ı kapat     |
| POST   | /network/usb-tethering/activate | Partial | Manuel olarak USB'ye geç          |
| POST   | /network/usb-tethering/deactivate | Partial | Manuel olarak PPPoE'ye dön      |

### Health Check
| Method | Path                            | Tür     | Açıklama                                    |
|--------|---------------------------------|---------|----------------------------------------------|
| GET    | /partials/healthcheck-status    | Partial | Tüm check'lerin güncel durumu (HTMX poll)    |
| PUT    | /network/healthcheck/config     | Partial | Health check ayarlarını güncelle              |
| POST   | /network/healthcheck/{name}/run | Partial | Tek bir check'i manuel çalıştır              |
| POST   | /network/healthcheck/{name}/reset | Partial | Failure counter'ı sıfırla                  |
| GET    | /events/healthcheck             | SSE     | Health check olay stream'i (durum değişikliği) |

### Firewall
| Method | Path                        | Tür     | Açıklama                      |
|--------|-----------------------------|---------|--------------------------------|
| GET    | /firewall                   | Sayfa   | Firewall kuralları sayfası     |
| GET    | /partials/fw-rules          | Partial | Kural listesi (HTMX swap)     |
| POST   | /firewall/port-forward      | Partial | Port yönlendirme ekle          |
| DELETE | /firewall/port-forward/{id} | Partial | Port yönlendirme sil           |
| POST   | /firewall/confirm           | Partial | Watchdog onay (30s timeout)    |
| PUT    | /firewall/ttl-fix           | Partial | TTL Fix aç/kapat + değer ayarla|

### DNS (Unbound)
| Method | Path                       | Tür     | Açıklama                          |
|--------|----------------------------|---------|------------------------------------|
| GET    | /dns                       | Sayfa   | DNS ayarları + istatistikler       |
| GET    | /partials/dns-stats        | Partial | DNS cache/query istatistikleri     |
| PUT    | /dns/config                | Partial | DNS ayarlarını güncelle            |
| POST   | /dns/blocklist/update      | Partial | Blocklist'i şimdi güncelle         |
| GET    | /partials/dns-blocklist    | Partial | Blocklist durumu + kaynak listesi  |
| GET    | /partials/dns-querylog     | Partial | Son DNS sorguları (filtreli, paginated) |
| GET    | /partials/dns-top-clients  | Partial | En çok sorgu yapan cihazlar        |
| GET    | /partials/dns-top-domains  | Partial | En çok sorgulanan domainler        |
| GET    | /partials/dns-top-blocked  | Partial | En çok engellenen domainler        |
| PUT    | /dns/querylog/toggle       | Partial | Query logging aç/kapat             |
| DELETE | /dns/querylog/clear        | Partial | Query log geçmişini temizle        |

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

### VPN Client (Outbound Tüneller)
| Method | Path                        | Tür     | Açıklama                              |
|--------|-----------------------------|---------|----------------------------------------|
| GET    | /vpn                        | Sayfa   | VPN yönetimi (client + server) sayfası |
| GET    | /partials/vpn-clients       | Partial | Client tünel listesi + durum           |
| POST   | /vpn/client                 | Partial | Yeni client tünel ekle                 |
| PUT    | /vpn/client/{name}          | Partial | Client tünel düzenle                   |
| DELETE | /vpn/client/{name}          | Partial | Client tünel sil                       |

### VPN Server (Inbound)
| Method | Path                            | Tür     | Açıklama                                    |
|--------|---------------------------------|---------|----------------------------------------------|
| GET    | /partials/vpn-server            | Partial | Server durumu + peer listesi                 |
| PUT    | /vpn/server/config              | Partial | Server ayarları (port, subnet, DNS)          |
| POST   | /vpn/server/toggle              | Partial | Server'ı aç/kapat                            |
| POST   | /vpn/server/peer                | Partial | Yeni peer ekle (keypair otomatik üret)       |
| PUT    | /vpn/server/peer/{name}         | Partial | Peer düzenle                                 |
| DELETE | /vpn/server/peer/{name}         | Partial | Peer sil                                     |
| GET    | /vpn/server/peer/{name}/config  | Download| Peer client config dosyası indir (.conf)     |
| GET    | /vpn/server/peer/{name}/qr      | Partial | Peer QR kodu (mobil WireGuard app için)      |

### OpenVPN Client (Outbound)
| Method | Path                              | Tür      | Açıklama                                     |
|--------|-----------------------------------|----------|-----------------------------------------------|
| GET    | /openvpn                          | Sayfa    | OpenVPN yönetimi (client + server) sayfası    |
| GET    | /partials/ovpn-clients            | Partial  | Client bağlantı listesi + durum               |
| POST   | /openvpn/client                   | Partial  | Yeni client ekle (.ovpn dosya import)         |
| DELETE | /openvpn/client/{name}            | Partial  | Client bağlantı sil                           |
| POST   | /openvpn/client/{name}/connect    | Partial  | Client bağlantısını başlat                    |
| POST   | /openvpn/client/{name}/disconnect | Partial  | Client bağlantısını kes                       |

### OpenVPN Server (Inbound)
| Method | Path                                   | Tür      | Açıklama                                  |
|--------|----------------------------------------|----------|---------------------------------------------|
| GET    | /partials/ovpn-server                  | Partial  | Server durumu + client listesi              |
| PUT    | /openvpn/server/config                 | Partial  | Server ayarları (port, protocol, cipher)    |
| POST   | /openvpn/server/toggle                 | Partial  | Server'ı aç/kapat                           |
| POST   | /openvpn/server/init-pki               | Partial  | PKI altyapısı oluştur (CA + server cert)    |
| POST   | /openvpn/server/client                 | Partial  | Yeni client sertifikası oluştur             |
| DELETE | /openvpn/server/client/{name}          | Partial  | Client sertifikasını revoke et              |
| POST   | /openvpn/server/client/{name}/toggle   | Partial  | Client'ı etkinleştir/devre dışı bırak       |
| GET    | /openvpn/server/client/{name}/config   | Download | Client .ovpn config dosyası indir           |
| GET    | /openvpn/server/client/{name}/qr       | Partial  | Client config QR kodu (mobil app için)      |

### Policy-Based Routing (PBR)
| Method | Path                          | Tür     | Açıklama                              |
|--------|-------------------------------|---------|----------------------------------------|
| GET    | /routing                      | Sayfa   | PBR politika yönetimi sayfası          |
| GET    | /partials/policies            | Partial | Politika listesi (sürükle-bırak sıralama) |
| POST   | /routing/policy               | Partial | Yeni politika ekle                     |
| PUT    | /routing/policy/{name}        | Partial | Politika düzenle                       |
| DELETE | /routing/policy/{name}        | Partial | Politika sil                           |
| PUT    | /routing/policy/{name}/toggle | Partial | Politikayı etkinleştir/devre dışı bırak|
| PUT    | /routing/reorder              | Partial | Politika sıralamasını güncelle (drag-drop) |
| GET    | /partials/policy-status       | Partial | Canlı eşleşme durumu (hangi cihaz hangi politikada) |
| GET    | /events/routing               | SSE     | PBR durum değişiklikleri (real-time)   |

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

### Syslog
| Method | Path                       | Tür     | Açıklama                           |
|--------|----------------------------|---------|------------------------------------|
| GET    | /syslog                    | Sayfa   | Syslog yapılandırma + log görüntüle|
| GET    | /partials/syslog-logs      | Partial | Uzak cihaz logları (filtreli, paginated) |
| PUT    | /syslog/server             | Partial | Sunucu ayarları (enable/disable, port, TLS) |
| PUT    | /syslog/client             | Partial | Client ayarları (remote host, protocol) |
| GET    | /partials/syslog-sources   | Partial | Log gönderen cihaz listesi         |

### NTP
| Method | Path                    | Tür     | Açıklama                               |
|--------|-------------------------|---------|-----------------------------------------|
| GET    | /ntp                    | Sayfa   | NTP yapılandırma + senkronizasyon durumu|
| GET    | /partials/ntp-status    | Partial | chrony sources + tracking durumu        |
| PUT    | /ntp/server             | Partial | NTP sunucu ayarları (enable/disable)    |
| PUT    | /ntp/client             | Partial | NTP client ayarları (upstream sunucular)|
| POST   | /ntp/force-sync         | Partial | Manuel zaman senkronizasyonu başlat     |

### System
| Method | Path                    | Tür     | Açıklama                          |
|--------|-------------------------|---------|------------------------------------|
| GET    | /settings               | Sayfa   | Sistem ayarları                    |
| PUT    | /settings/system        | Partial | Hostname, timezone güncelle        |
| PUT    | /settings/password      | Partial | Şifre değiştir                     |
| GET    | /partials/tls-status    | Partial | TLS sertifika durumu (mod, expire) |
| PUT    | /settings/tls           | Partial | TLS modu değiştir + ayarlar        |
| POST   | /settings/tls/generate  | Partial | Sertifika yeniden oluştur          |
| GET    | /settings/tls/ca        | Dosya   | mkcert CA sertifikası indir (.crt) |
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
    openvpn easy-rsa \
    samba samba-common-bin \
    smartmontools mdadm \
    iproute2 \
    unbound \
    dnsmasq \
    rsyslog \
    chrony \
    qrencode \                   # WireGuard peer QR kodu üretimi
    wide-dhcpv6-client \           # DHCPv6-PD prefix delegation client
    mkcert                        # Lokal CA + güvenilir sertifika (opsiyonel TLS modu)

# dnsmasq: DNS kapalı (port=0), sadece DHCP
# unbound: recursive DNS resolver + blocklist
# rsyslog: syslog sunucu (ağdan log alma) + client (log forwarding)
# chrony: NTP sunucu (LAN cihazlarına zaman servisi) + client (upstream senkronizasyon)
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
- `deploy/systemd/home-router-agent.service`
- `deploy/systemd/home-router-web.service`
- `deploy/systemd/home-router.target`
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
    - `homerouter` sistem kullanıcısı oluştur (nologin, /opt/home-router home)
    - Binary'yi `/usr/local/bin/home-router` altına kopyala + `chmod +x`
    - systemd unit dosyalarını `/etc/systemd/system/` altına yerleştir + `systemctl enable`
    - udev rules: NIC isimlendirme (MAC tabanlı), USB tethering RNDIS → `/etc/udev/rules.d/`
    - Config dizini: `/etc/home-router/` oluştur, varsayılan YAML config'leri kopyala
    - Veri dizini: `/var/lib/home-router/` (TLS sertifikaları, credentials)
    - Log dizini: `/var/log/home-router/`, `/var/log/unbound/`
    - sysctl parametreleri: `/etc/sysctl.d/99-home-router.conf` (ip_forward, rp_filter, syncookies, ipv6 forwarding)
    - İlk admin şifresi: interaktif `read -s` ile al → bcrypt hash → config'e yaz
    - İlk TLS sertifikası: self-signed otomatik üretim
    - Unbound/dnsmasq/chrony başlangıç config'leri render
    - `systemctl start home-router.target`
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
     - Sertifika: `/var/lib/home-router/tls/server.crt` + `server.key`
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
- **TLS self-signed:** ilk başlatmada sertifika otomatik üretildi mi (`/var/lib/home-router/tls/`)
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
   - **QR kodu (mobil cihazlar için):**
     - Go'da QR üretimi: `go-qrcode` kütüphanesi veya `exec.Command("qrencode")`
     - Client config string → QR PNG → base64 → `<img>` tag ile web UI'da göster
     - WireGuard mobil app: QR kodu okut → tek tıkla bağlan
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
1. ✅ RAID: `mdadm --detail` parse, degraded alarm
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
   - **Disk bölümleme (RAID-1):**
     - İki SSD algıla (`/dev/sda`, `/dev/sdb`)
     - `mdadm` ile RAID-1 array oluştur (`/dev/md0`)
     - Partition layout: `/boot` (512MB, ext4, RAID-1), `/` (kalan, ext4, RAID-1), swap (4GB, RAID-1)
     - UEFI + legacy BIOS dual-boot (EFI partition her iki diskte)
   - Paket seçimi: `standard`, `ssh-server` (minimal, GUI yok)
   - Root hesabı devre dışı, `homerouter` kullanıcısı oluştur
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
   - İlk boot'ta web UI setup wizard için flag oluştur (`/var/lib/home-router/.first-boot`)
   - GRUB: RAID-1 aware, degraded boot allowed

4. **ISO build scripti (`build-iso.sh`):**
   - Girdi: resmi Debian 12 netinst ISO + Go binary (cross-compile edilmiş)
   - Çıktı: `home-router-installer.iso` (custom preseed + binary gömülü)
   - İşlem:
     - Debian ISO'yu aç (`xorriso` veya `bsdtar`)
     - `preseed.cfg` + `post-install.sh` + Go binary'yi ISO'ya ekle
     - GRUB config güncelle: `auto=true preseed/file=/cdrom/preseed.cfg`
     - ISO'yu yeniden oluştur (`xorriso -as mkisofs`)
     - Opsiyonel: USB yazılabilir hybrid ISO (`isohybrid`)
   - Makefile entegrasyonu: `make iso` → cross-compile + ISO build
   - CI/CD: GitHub Actions'da ISO build (release artifact olarak)

5. **İlk Boot Setup Wizard:**
   - `/var/lib/home-router/.first-boot` dosyası varsa web UI'da setup wizard göster
   - Wizard adımları:
     - Admin şifresi belirleme
     - WAN interface seçimi + PPPoE credentials (veya DHCP)
     - LAN interface seçimi + subnet
     - Temel DNS ayarları (recursive veya forwarder)
   - Wizard tamamlandığında `.first-boot` silinir, normal dashboard gösterilir
   - Wizard atlanabilir (ileri düzey kullanıcı doğrudan config düzenler)

6. **Makefile hedefleri:**
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
| Health check reboot döngüsü           | Cooldown süresi + max reboot count/24h limiti + reboot sonrası grace period |
| VPN server private key sızması        | AES-256-GCM at rest, peer config indirmede one-time token, QR timeout       |
| VPN server WAN IP değişimi (PPPoE)    | DDNS desteği (configurable hostname), ip-up script ile DDNS güncelleme      |
| DNS query log disk dolması            | logrotate (maxSize + retention), ring buffer in-memory, toggle ile kapatılabilir |
| OpenVPN PKI private key sızması      | CA/server key /etc/openvpn/pki/ (700 perms), backup'ta AES-256-GCM encrypt      |
| OpenVPN DH parametresi üretimi yavaş | `easyrsa gen-dh` arka planda, UI'da ilerleme göstergesi, ~2-5dk (i5 3470)        |
| ISP IPv6 desteği yok/kısıtlı        | `ipv6.enabled: auto` → IPv6CP başarısızsa IPv4-only, ULA ile LAN içi IPv6 korunur |
| DHCPv6-PD prefix değişimi (PPPoE)    | PPPoE reconnect sonrası yeni prefix → LAN'a RA ile dağıtım, geçiş süresi ~30s    |
| ICMPv6 engellenmesi → IPv6 çalışmaz | RFC 4890 zorunlu allowlist (NDP, MLD, error messages) — asla drop edilmez          |
| IPv6 privacy extension tracking      | RA'da privacy extension önerisi (RFC 4941), temporary addresses                    |
| Self-signed cert tarayıcı uyarısı   | mkcert modu ile LAN'da güvenilir CA, ACME ile public domain desteği                |
| ACME DNS challenge API key sızması   | Token `.credentials.enc`'de AES-256-GCM, agent op whitelist                        |
| Let's Encrypt rate limit (5/hafta)  | Staging ortamı ile test, production'da dikkatli kullanım                            |
| USB tethering telefon bağlı değil   | failoverUsb aksiyonu telefon yoksa atlanır, sonraki aksiyona geçilir                |
| Mobil operatör tethering algılama    | USB üzerinden TTL Fix (ayrı toggle), hotspot tespiti bypass                        |
| USB tethering bant genişliği düşük  | Yedek amaçlı — sadece temel bağlantı, QoS/VPN devre dışı bırakılabilir            |
| USB interface ismi değişkenliği     | udev rule RNDIS class match (vendor-agnostic), Samsung/Xiaomi/Google test          |
| Preseed RAID-1 disk sırası değişir | Preseed'de disk serial/ID ile eşleştirme, `/dev/disk/by-id/` kullanımı             |
| ISO build reproducibility           | Makefile + pinned Debian ISO checksum + Go binary hash                             |
| UEFI Secure Boot + RAID-1          | Preseed'de EFI partition her iki diskte, `grub-install` her iki diske              |

## Tahmini Toplam Süre

| Phase | Konu                                                    | Gün | Kümülatif |
|-------|---------------------------------------------------------|-----|-----------|
| 1     | İskelet + Agent IPC                                     | 3   | 3         |
| 2     | Web + Auth + HTMX Layout                                | 3   | 6         |
| 3     | Network + VLAN + PPPoE + USB Tethering + IPv6 + Health  | 8   | 14        |
| 4     | nftables Firewall + NAT + IPv6                          | 5   | 19        |
| 5     | Unbound DNS + DHCP + Query Logging + IPv6 RA            | 5   | 24        |
| 6     | Dashboard + SSE                                         | 3   | 27        |
| 7     | SQM/QoS                                                 | 3   | 30        |
| 8     | WireGuard + OpenVPN + PBR                               | 11  | 41        |
| 9     | Samba NAS + M3U                                         | 3   | 44        |
| 10    | Storage + Syslog + NTP + Backup                         | 5   | 49        |
| 11    | Deployment — install.sh + Preseed ISO                   | 3   | 52        |

**Toplam: ~52 geliştirme günü** (tek geliştirici, her gün 4-6 saat efektif çalışma varsayımı)
