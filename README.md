# jobctl

[![CI](https://github.com/ivolejon/jobctl/actions/workflows/ci.yml/badge.svg)](https://github.com/ivolejon/jobctl/actions/workflows/ci.yml)

`jobctl` kör skaljobb från en versionshanterad `jobs.toml` på Linux och macOS. Kör ett jobb direkt eller schemalägg det med systemd eller launchd, utan en egen daemon. Manuella och schemalagda körningar delar samma lås, tidsgräns, logg och körningskvitto.

## Bygg och prova

Go 1.23 eller senare behövs för att bygga. TOML-biblioteket `github.com/pelletier/go-toml/v2` är det enda tredjepartsberoendet och byggs in i binären. Ingen Go-installation behövs vid körning. Schemaläggning kräver en tillgänglig användarinstans av systemd respektive launchd.

```sh
git clone https://github.com/ivolejon/jobctl.git
cd jobctl
CGO_ENABLED=0 go install ./cmd/jobctl
```

Se till att Go:s installationskatalog (normalt `$(go env GOPATH)/bin`) finns i `PATH`. Kör sedan från repots rot, där `jobs.toml` innehåller jobbet `demo`:

```sh
jobctl validate
jobctl list
jobctl run demo --json
jobctl status demo
jobctl logs demo
jobctl logs demo --path
jobctl install demo --print
jobctl version
```

När du vill aktivera schemat på din maskin, kör `jobctl install demo`. Ta bort det med `jobctl uninstall demo`. Använd en binär på en beständig plats för schemalagda jobb.

Demo-jobbet skriver `.jobctl-demo.txt` i konfigurationens katalog. `list` och `status` har även `--json`. Med `run --dry-run` kan du granska kommandot, arbetskatalogen, miljövariabelnamnen, tidsgränsen och låsen utan att starta jobbet eller skriva till state. Kör `jobctl run --wait demo --wait-timeout 5m` för att vänta på lås; standard är att inte vänta alls.

## Konfiguration

Valordning: `--config PATH`, `$JOBCTL_CONFIG`, `./jobs.toml`, sedan `$XDG_CONFIG_HOME/jobctl/jobs.toml` (standard `~/.config/jobctl/jobs.toml`). `--config` och `--state-dir` kan anges före eller efter subkommandot. Subkommandots egna flaggor kan anges på båda sidor om jobbnamnet. Om namnet börjar med `-`, sätt flaggorna först och avsluta dem med `--`. Relativa `cwd`- och `env_file`-sökvägar utgår från konfigurationsfilens katalog. `~` och `~/…` expanderas till den aktuella användarens hemkatalog.

```toml
[jobs.nightly-gate]
description = "Nattlig testkörning"
command = "scripts/nightly-gate.sh"
cwd = "."
schedule = "daily 02:00"
timeout = "3h"
kill_grace = "30s"
lock = ["heavy"]
retention_days = 30
env = { CI = "1" }
env_file = "~/.secrets"
nice = 10
io_idle = true

[jobs.web-unit]
command = "npm test"
cwd = "apps/web"
schedule = "every 30m"
timeout = "10m"
```

- Jobb- och låsnamn måste matcha `[a-z0-9-]+`. `command` är obligatoriskt. `cwd` måste finnas och är som standard konfigurationsfilens katalog.
- `timeout` och `kill_grace` är positiva Go-varaktigheter, som standard `1h` och `30s`. `lock` kan vara ett namn eller en lista; som standard låses jobbnamnet. `lock = []` väljer bort låsning. Flera lås tas i sorterad ordning. Med `--wait` är standardgränsen `30m` för alla lås tillsammans.
- `env_file` läses som text, inte som skal: en `KEY=VALUE` per rad. Tomma rader och rader som börjar med `#` (efter eventuellt inledande blanksteg) ignoreras. Värdet efter första `=` används bokstavligt. Ordningen är ärvd miljö, `env_file`, sedan `env`; den sista definitionen vinner. `validate` kontrollerar också miljöfilerna.
- `enabled` är `true` som standard. Inaktiva jobb kan köras manuellt men hoppas över vid `install` utan namn, och körningar med `--trigger schedule` blockeras. Avinstallera tidigare skapade enheter uttryckligen; ändringar i TOML tar inte bort dem.
- `validate` ger fil och rad för varje valideringsfel, inklusive okända fält. Fel i själva TOML-syntaxen (eller motstridiga TOML-nycklar) kan göra att avkodaren stannar vid första felet; rätta det först och kör `validate` igen.

Konfigurationen innehåller **betrodda** kommandon. Lägg inte hemligheter i TOML eller direkt i kommandoraden: kommandot skrivs ordagrant i kvittot. Lägg känsliga miljövärden i en separat, åtkomstskyddad miljöfil utanför versionskontrollen. `jobctl` skriver inte dessa värden, men `output.log` innehåller jobbets stdout och stderr ordagrant: jobbet får inte skriva ut hemligheter.

## Schema och installation

| Schema | Exempel | Betydelse |
| --- | --- | --- |
| `daily HH:MM` | `daily 02:00` | Varje dag |
| `weekly DAG[,DAG...] HH:MM` | `weekly mon,thu 02:00` | Angivna veckodagar (`mon`–`sun`, utan dubbletter) |
| `every N[s/m/h]` | `every 30m` | Intervall med ett positivt heltal i sekunder, minuter eller timmar |

Kalenderscheman använder maskinens lokala tidszon. `status` visar en uppskattning, inte ett svar från schemaläggaren. Intervall räknas fram från den senaste registrerade starten (även manuell), eller från nu om ingen körning finns. Inloggning, vila, överlappande körningar och sommartid kan påverka det faktiska utfallet.

`install --print` skriver enhetsfilerna till stdout utan att skapa filer eller anropa schemaläggaren. Vid installation lagras både en kopia under `generated/` i state och en installerad fil i schemaläggarens katalog. Enheterna använder absoluta sökvägar till binär, konfiguration och state och skickar `--trigger schedule`. Flyttar du någon av dessa måste enheten installeras på nytt. `--force` krävs för att ersätta en omärkt fil; `uninstall` tar inte bort omärkta filer. `install` och `uninstall` utan namn utgår från jobben i den aktuella konfigurationen. Om ett aktivt jobb saknar schema, ange uttryckligen vilka andra jobb du vill installera. Avinstallera ett jobb innan du tar bort det från TOML. Vid ett fel från schemaläggaren kan filerna redan ha skrivits; läs diagnostiken och kontrollera enheternas tillstånd innan du försöker igen.

- **Linux:** `~/.config/systemd/user/jobctl-<namn>.{service,timer}`. Användarenheter körs normalt bara medan användarens session är aktiv. För körning utan inloggning: konfigurera `loginctl enable-linger <användare>` själv. Kalenderbaserade timers har `Persistent=true` och kan ta igen en missad körning när timern åter aktiveras; de kör inte alla missade tillfällen. Intervalltimers använder `OnActiveSec` för första starten och `OnUnitActiveSec` därefter, utan `Persistent`. Tjänsten använder `TimeoutStartSec=infinity` så att tidsgränsen ägs av `jobctl`. `ExecStart=:` stänger av systemd:s variabelexpansion av argumenten. Arbetskataloger med radbrytningar, avslutande blanksteg eller avslutande omvänt snedstreck stöds inte i `WorkingDirectory=`; installationen avvisar dem i stället för att skriva en felaktig enhet.
- **macOS:** `~/Library/LaunchAgents/com.jobctl.<namn>.plist`, i användarens `gui/<uid>`-domän. Avinstallation använder launchctls tjänsteadress `gui/<uid>/com.jobctl.<namn>` som ett argument till `bootout`. launchd kör inte missade tider efter omstart. Ett kalendertillfälle som missas under vila kan däremot köras vid uppvaknande. Intervall är `StartInterval` i sekunder och har ingen återhämtning efter omstart.
- `nice` skickas till respektive schemaläggare (inom OS:ets egna gränser); `io_idle` används bara av systemd. Dessa inställningar ändrar inte den manuella processens prioritet.

## Körningar och tillstånd

Tillståndets rot är `--state-dir PATH`, `$JOBCTL_STATE_DIR` eller `$XDG_STATE_HOME/jobctl` (standard `~/.local/state/jobctl`). Välj en privat katalog. `locks/` innehåller beständiga flock-filer; radera dem inte. Under `runs/<jobb>/<run-id>/` sparas `output.log` (`0600`) och det atomiskt skrivna `meta.json`. `latest` pekar på senaste körningen; saknade eller inaktuella länkar hanteras genom att läsa körningskatalogerna. Efter en körning rensas äldre avslutade körningar för samma jobb efter `retention_days` (standard 30 dygn). `0` behåller den aktuella körningen men kan rensa tidigare avslutade körningar direkt. Kvitton som står kvar som `running` efter exempelvis strömavbrott rensas inte automatiskt.

Jobbet körs med `/bin/sh -c` i en egen processgrupp. Vid timeout skickas `SIGTERM`; vid `SIGINT` eller `SIGTERM` till `jobctl` vidarebefordras signalen. Efter `kill_grace` får kvarvarande gruppmedlemmar `SIGKILL`. Processer som själva lämnar gruppen omfattas inte av detta. Ett körningskvitto visar utfallet (`passed`, `failed`, `timed_out`, `interrupted` eller `start_failed`) och barnets faktiska exitkod eller signal. En pågående körning visas som `running`. Inget kvitto skapas om lås inte kan tas.

`jobctl run` lämnar över jobbets egen exitkod, även om den sammanfaller med `jobctl`:s koder. Kontrollera kvittots `status` vid överlapp. `jobctl` använder `124` för timeout, `125` om skalprocessen inte kan startas, `126` för låskonflikt, `130`/`143` för avbrott från SIGINT/SIGTERM, `64` för användningsfel, `78` för konfigurationsfel och `70` för interna fel. Ett jobb som självt dör av en signal ger `128 + signalnummer` och status `failed`. Diagnostik och den vanliga `run`-sammanfattningen går till stderr; exempelvis JSON, logginnehåll och `install --print` går till stdout.

## Verifiering

```sh
export CGO_ENABLED=0
go vet ./...
go test ./...
go build ./...
for os in linux darwin; do
  for arch in amd64 arm64; do
    GOOS=$os GOARCH=$arch go build -o "/tmp/jobctl-$os-$arch" ./cmd/jobctl
  done
done
```

CI kör på Linux och macOS med Go 1.23. Integrationstesterna använder temporära kataloger och stubbar för `systemctl` och `launchctl`. Båda plattformarnas filformat jämförs mot golden-filer på varje plattform. Kör binären på avsedd plattform; Darwin-byggen använder macOS systembibliotek, men inget CGo eller externt paketerat bibliotek.

## Licens

[MIT](LICENSE).
