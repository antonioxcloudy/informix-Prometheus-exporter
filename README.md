# Prometheus Informix Exporter

Exporter de métricas de HCL/IBM Informix para Prometheus, escrito en Go y conectado mediante ODBC.

## Componentes

La instalación completa de Docker Compose incluye:

- Informix exporter (todas las métricas): <http://localhost:8080/metrics>
- Prometheus: <http://localhost:9090>
- Alertmanager: <http://localhost:9093>
- Grafana: <http://localhost:3000>

Grafana provisiona el dashboard **Informix - Vista general** dentro de la carpeta `Informix`. El acceso directo es <http://localhost:3000/d/informix-overview/informix-vista-general>.

La imagen del exporter contiene el binario, unixODBC y las bibliotecas de compatibilidad necesarias, pero **no contiene el Informix Client SDK**. Un servicio de preparación importa el runtime propietario desde un TAR local a un volumen Docker persistente. Así no se guarda en Git ni en las capas de la imagen y, en Docker Desktop, se conservan correctamente los permisos y enlaces de Linux.

## Requisitos

- Docker Engine con Docker Compose v2.
- Un TAR del runtime de Informix Client SDK para Linux x86-64.
- Acceso TCP desde Docker hasta las instancias Informix.
- Un usuario Informix de solo lectura con acceso a `sysmaster`.

El TAR debe conservar esta estructura:

```text
lib/cli/iclit09b.so
lib/esql/libifgls.so
lib/esql/libifglx.so
gls/
msg/
```

La instalación utiliza `linux/amd64` por defecto. Si dispones de un runtime para otra arquitectura, cambia `EXPORTER_PLATFORM` en `.env`.

Los cuatro puertos se publican en `0.0.0.0` para que el servidor de monitorización sea accesible desde otros equipos. Limita los puertos 8080, 9090, 9093 y 3000 mediante firewall a las redes de administración; si el acceso debe ser exclusivamente local o mediante un proxy inverso instalado en el mismo servidor, configura `MONITORING_BIND_ADDRESS=127.0.0.1`.

## Instalación con Docker Compose

### Instalación asistida

El asistente crea únicamente los ficheros que todavía no existen y nunca sobrescribe una configuración local. En PowerShell:

```powershell
.\scripts\install.ps1 -Action Prepare -Secrets
```

En Linux/macOS:

```bash
./scripts/install.sh prepare --secrets
```

Después de editar `.env`, `deploy/config/config.yaml`, `deploy/config/odbc.ini`, `deploy/config/sqlhosts`, `deploy/config/alertmanager.yml` y `deploy/secrets/informix_password`, valida la instalación sin conectarte todavía a Informix:

```powershell
.\scripts\install.ps1 -Action Validate
```

```bash
./scripts/install.sh validate
```

Finalmente, importa el Client SDK, comprueba el driver, conecta con Informix, ejecuta todos los colectores y levanta el stack:

```powershell
.\scripts\install.ps1 -Action Start
```

```bash
./scripts/install.sh start
```

El asistente detecta automáticamente `PWD_FILE` y utiliza `compose.secrets.yaml`. Antes de arrancar rechaza contraseñas vacías o el valor inicial de Grafana, conserva el volumen CSDK existente y espera a que los servicios estén preparados. También puede usarse sin Docker Secrets omitiendo `-Secrets` o `--secrets` durante la preparación.

Los pasos manuales siguientes explican los mismos cambios y siguen siendo útiles para instalaciones personalizadas.

### 1. Preparar los ficheros locales

PowerShell:

```powershell
Copy-Item .env.example .env
Copy-Item examples/config.yaml deploy/config/config.yaml
Copy-Item examples/odbc.ini deploy/config/odbc.ini
Copy-Item examples/sqlhosts deploy/config/sqlhosts
```

Linux/macOS:

```bash
cp .env.example .env
cp examples/config.yaml deploy/config/config.yaml
cp examples/odbc.ini deploy/config/odbc.ini
cp examples/sqlhosts deploy/config/sqlhosts
```

Estos cuatro ficheros están ignorados por Git.

### 2. Guardar e importar el runtime de Informix

Guarda el fichero como:

```text
runtime/informix-client-runtime-linux-amd64.tar.gz
```

Si utilizas otro nombre o ubicación, cambia `INFORMIX_CSDK_ARCHIVE` en `.env`. Después impórtalo una sola vez al volumen Docker:

```bash
docker compose --profile setup run --rm csdk-init
```

El proceso valida las rutas y los ficheros imprescindibles antes de marcar el runtime como instalado. Repetir el comando con el mismo TAR es seguro. El servicio rechazará sobrescribir el volumen si el TAR cambia.

### 3. Configurar los DSN de Informix

Edita `deploy/config/odbc.ini`:

```ini
[ODBC]
UNICODE=UCS-4

[informix-production]
Driver=/opt/informix/lib/cli/iclit09b.so
ServerName=ol_production
Database=sysmaster
UID=prometheus
PWD=change-me
```

Edita también `deploy/config/sqlhosts` con la dirección del servidor:

```text
ol_production onsoctcp informix.example.net 1526
```

`ServerName` debe coincidir con la primera columna de `sqlhosts` y con el valor de `INFORMIXSERVER` de la instancia. No guardes los ficheros reales en Git.

### 4. Configurar las instancias del exporter

Edita `deploy/config/config.yaml`:

```yaml
servers:
  - name: informix-production
    informixserver: informix-production

custom: []

table_limits:
  minimum_extents: 100
  minimum_partition_pages: 8000000
  classic_partition_page_limit: 16777215
```

`informixserver` debe coincidir exactamente con el nombre de la sección DSN de `odbc.ini`. Las credenciales solo se declaran en `odbc.ini`; no se duplican en el YAML.

Los colectores se pueden seleccionar para adaptar el coste del scrape a cada instalación:

```yaml
collectors:
  - profile
  - activity
  - dbspaces
  - custom
```

Los nombres disponibles son `profile`, `activity`, `dbspaces`, `tables` y `custom`. Si se omite completamente `collectors`, todos quedan habilitados para conservar la compatibilidad. Para aparcar la monitorización de límites de tablas basta con no incluir `tables`; `custom` solo ejecuta consultas cuando existe alguna entrada en esa sección.

La instalación separa las consultas por frecuencia para no consultar datos casi estáticos cada minuto:

```yaml
slow_collectors:
  - dbspaces
  - tables
```

- `/metrics/fast`: rendimiento, actividad e I/O de chunks; Prometheus lo consulta cada minuto.
- `/metrics/storage`: capacidad de dbspaces y límites de tablas; consulta Informix al arrancar y después cada dos horas.
- `/metrics`: conserva todas las métricas en un único scrape para compatibilidad y diagnóstico manual.

`dbspaces` se divide internamente: su capacidad y estado pertenecen al grupo lento, pero los contadores de I/O de chunks permanecen en el grupo rápido. Si se omite `slow_collectors`, se usan `dbspaces` y `tables` como valores predeterminados. Una lista vacía (`slow_collectors: []`) devuelve todos los colectores al grupo rápido. Prometheus recoge el snapshot lento cada minuto para disponer de datos desde el arranque, pero el exporter lo sirve desde memoria y solo repite las consultas a Informix cada dos horas. El intervalo puede cambiarse con `EXPORTER_SLOW_REFRESH_INTERVAL` en `.env`.

#### Varias instancias

Cada servidor referencia su propia sección DSN. Los servidores se consultan concurrentemente con una única conexión ODBC por instancia, por lo que una instancia lenta o caída no consume el tiempo disponible para las demás:

```yaml
servers:
  - name: informix-production
    informixserver: informix-production

  - name: informix-reporting
    informixserver: informix-reporting
    collectors:
      - activity
      - dbspaces

collectors:
  - profile
  - activity
  - dbspaces
  - custom
```

La lista dentro de un servidor sustituye a la lista global solo para esa instancia; si se omite, hereda la global. Los nombres `name` y `informixserver` deben ser únicos. Las plantillas completas están en `examples/config.multi-instance.yaml`, `examples/odbc.multi-instance.ini` y `examples/sqlhosts.multi-instance`.

La salud y duración se publican por instancia en `informix_exporter_instance_scrape_success`, `informix_exporter_instance_scrape_duration_seconds` e `informix_exporter_instance_last_successful_scrape_timestamp_seconds`. La telemetría de collectors también incluye `informixserver`. `/readyz` comprueba el grupo rápido y `/readyz/storage` el grupo lento; `informix_exporter_scrape_success` solo vale `1` cuando todas las instancias del grupo terminan bien.

Las métricas de límites suman los extents de todas las particiones de cada tabla. Las métricas por partición se publican únicamente para vigilar el límite clásico de páginas/ROWID. Los dos mínimos filtran tablas alejadas de esos límites y evitan una cardinalidad excesiva; usa `-1` si necesitas exportarlas todas. En tablas `LARGE`, el límite clásico de páginas no es aplicable y la alerta debe interpretarse teniendo en cuenta ese tipo de tabla.

#### Contraseña mediante Docker Secrets (recomendado)

Para no guardar la contraseña en `deploy/config/odbc.ini`, utiliza la plantilla preparada:

```powershell
Copy-Item examples/odbc.secrets.ini deploy/config/odbc.ini
New-Item -ItemType Directory -Force deploy/secrets/informix
Set-Content -NoNewline deploy/secrets/informix_password "tu-contraseña"
```

En Linux/macOS, crea el secreto sin salto de línea:

```bash
cp examples/odbc.secrets.ini deploy/config/odbc.ini
mkdir -p deploy/secrets/informix
printf '%s' 'tu-contraseña' > deploy/secrets/informix_password
chmod 600 deploy/secrets/informix_password
```

Arranca Compose con el fichero adicional:

```bash
docker compose -f compose.yaml -f compose.secrets.yaml up -d
```

`PWD_FILE` es una extensión del entrypoint, no una opción del driver ODBC. Se sustituye dentro del contenedor por `PWD` usando un fichero temporal con permisos restrictivos. El directorio `deploy/secrets/` está ignorado por Git.

Para credenciales diferentes por instancia, parte de `examples/odbc.multi-instance.secrets.ini` y guarda cada contraseña en el directorio aislado que monta `compose.secrets.yaml`:

```text
deploy/secrets/informix/production_password
deploy/secrets/informix/reporting_password
```

Las rutas correspondientes dentro de `odbc.ini` son `/run/informix-secrets/production_password` y `/run/informix-secrets/reporting_password`. El instalador valida todos los ficheros referenciados antes de arrancar.

### 5. Establecer la contraseña de Grafana

Edita `.env` y cambia obligatoriamente:

```dotenv
GRAFANA_ADMIN_PASSWORD=una-contraseña-segura
```

### 6. Validar, construir y arrancar

```bash
docker compose config --quiet
docker compose build informix-exporter
docker compose up -d
docker compose ps
```

Si ya tienes Prometheus y Grafana, puedes arrancar únicamente el exporter:

```bash
docker compose up -d informix-exporter
```

Consulta sus logs con:

```bash
docker compose logs -f informix-exporter
```

## Comprobaciones

Versión del binario o de la imagen, sin necesitar Client SDK:

```bash
docker compose run --rm informix-exporter --version
```

Validación exclusiva del YAML, sin abrir conexiones:

```bash
docker compose run --rm informix-exporter \
  --configfile=/etc/informix-exporter/config.yaml \
  --check-config
```

Validación del Client SDK, `odbc.ini`, `sqlhosts` y ficheros de contraseña:

```bash
docker compose run --rm informix-exporter --check-runtime
```

Diagnóstico completo de instalación. Abre cada DSN y ejecuta las mismas consultas que un scrape, incluidas las métricas personalizadas:

```bash
docker compose run --rm informix-exporter \
  --configfile=/etc/informix-exporter/config.yaml \
  --scrape-timeout=8s \
  --check
```

El comando termina con código distinto de cero ante cualquier fallo, por lo que puede utilizarse en automatizaciones de despliegue.

La versión desplegada también se publica en la métrica `informix_exporter_build_info` mediante las etiquetas `version`, `commit` y `build_date`. El resultado y tiempo individual de cada colector se exponen como `informix_exporter_collector_success{informixserver="...",collector="..."}` e `informix_exporter_collector_duration_seconds{informixserver="...",collector="..."}`.

El catálogo completo de las 67 familias incorporadas está en [`docs/metrics.md`](docs/metrics.md). Las consultas personalizadas pueden añadir además métricas `informix_custom_*`.

Salud del proceso HTTP:

```bash
curl http://localhost:8080/healthz
```

Disponibilidad real (devuelve HTTP 503 hasta completar al menos un scrape correcto):

```bash
curl --fail http://localhost:8080/readyz
curl --fail http://localhost:8080/readyz/storage
```

Métricas:

```bash
curl http://localhost:8080/metrics
curl http://localhost:8080/metrics/fast
curl http://localhost:8080/metrics/storage
```

Prueba manual del DSN dentro del contenedor:

```bash
docker compose exec informix-exporter isql -v informix-production
```

Para comprobar las dependencias del driver, la imagen incluye el cargador dinámico de Debian:

```bash
docker compose exec informix-exporter \
  /lib64/ld-linux-x86-64.so.2 --list /opt/informix/lib/cli/iclit09b.so
```

## Configuración de Prometheus y Grafana

Prometheus se configura desde `deploy/prometheus/prometheus.yml` y consulta `informix-exporter:8080` cada minuto. Alertmanager continúa comprobándose cada 15 segundos. El datasource provisionado de Grafana declara el mismo intervalo de un minuto para que las consultas de tasas utilicen una ventana adecuada.

Prometheus envía las alertas a Alertmanager, disponible en el puerto 9093. La configuración local `deploy/config/alertmanager.yml` agrupa por alerta e instancia, espera 30 segundos antes de la primera notificación, repite avisos críticos cada hora y el resto cada cuatro horas. Una alerta crítica inhibe los avisos de nivel warning de la misma instancia.

Los receptores iniciales no entregan mensajes fuera del sistema: permiten comprobar alertas, agrupaciones y silencios desde la interfaz de Alertmanager sin generar errores por credenciales inexistentes. Añade al fichero local una integración `email_configs`, `webhook_configs`, Slack, Teams u otro receptor cuando se decida el canal; el fichero está ignorado por Git para no publicar secretos.

#### Webhook genérico opcional

El overlay `compose.webhook.yaml` cambia los receptores internos por un webhook genérico sin guardar su URL en Git. Cuando exista el endpoint, guarda únicamente la URL en el secreto local.

PowerShell:

```powershell
$webhookUrl = Read-Host "URL del webhook"
Set-Content -NoNewline deploy/secrets/alertmanager_webhook_url $webhookUrl
Remove-Variable webhookUrl
.\scripts\install.ps1 -Action Validate -Webhook
docker compose -f compose.yaml -f compose.webhook.yaml up -d --force-recreate alertmanager
```

Linux/macOS:

```bash
read -r -s -p "URL del webhook: " webhook_url && echo
printf '%s' "$webhook_url" > deploy/secrets/alertmanager_webhook_url
chmod 600 deploy/secrets/alertmanager_webhook_url
unset webhook_url
./scripts/install.sh validate --webhook
docker compose -f compose.yaml -f compose.webhook.yaml up -d --force-recreate alertmanager
```

El webhook recibe peticiones HTTP `POST` con el JSON estándar de Alertmanager, incluye como máximo 20 alertas por grupo, espera hasta 10 segundos y también comunica las resoluciones. En instalaciones nuevas se puede usar directamente `Start -Webhook` o `start --webhook`.

Grafana provisiona automáticamente Prometheus como datasource predeterminado. Sus datos y los de Prometheus se almacenan en volúmenes Docker persistentes.

## Operación, copias y actualizaciones

La copia predeterminada guarda la configuración local, los secretos y los volúmenes de Grafana y Alertmanager. Ambos servicios se detienen durante unos segundos para obtener una instantánea consistente; el exporter y Prometheus continúan funcionando. Se conservan las siete copias más recientes:

```powershell
.\scripts\backup.ps1
```

```bash
./scripts/backup.sh
```

El histórico de Prometheus suele ser voluminoso y se puede reconstruir mediante nuevos scrapes, por eso no se incluye por defecto. Para conservarlo también, Prometheus se detendrá mientras se copia su volumen:

```powershell
.\scripts\backup.ps1 -IncludePrometheus -Retain 14
```

```bash
./scripts/backup.sh --include-prometheus --retain 14
```

Cada directorio de `backups/` contiene un manifiesto y hashes SHA-256. `config.tar.gz` incluye `.env`, credenciales ODBC y secretos: debe custodiarse como una copia de contraseñas y nunca publicarse. El directorio completo está ignorado por Git.

La restauración verifica primero todos los hashes, apaga el stack y reemplaza solamente los volúmenes presentes en la copia. Requiere confirmación escribiendo `RESTORE`; la configuración solo se sobrescribe al pedirlo explícitamente:

```powershell
.\scripts\restore.ps1 -Backup .\backups\20260830T120000Z -ValidateOnly
.\scripts\restore.ps1 -Backup .\backups\20260830T120000Z -Start
.\scripts\restore.ps1 -Backup .\backups\20260830T120000Z -RestoreConfig -Start
```

```bash
./scripts/restore.sh --backup ./backups/20260830T120000Z --validate-only
./scripts/restore.sh --backup ./backups/20260830T120000Z --start
./scripts/restore.sh --backup ./backups/20260830T120000Z --restore-config --start
```

Si Alertmanager utiliza el overlay del webhook, añade `-Webhook` en PowerShell o `--webhook` en Bash también durante la restauración.

La actualización controlada valida Prometheus y Alertmanager, crea una copia previa, descarga las imágenes fijadas en Compose, reconstruye el exporter y espera a que respondan los cuatro endpoints. Antes del cambio conserva cada imagen anterior bajo una etiqueta local de rollback; si el proceso falla, vuelve a etiquetar y levantar automáticamente esas versiones:

```powershell
.\scripts\update.ps1
```

```bash
./scripts/update.sh
```

Usa `-Webhook` o `--webhook` si ese overlay está activo. El rollback de la actualización afecta a imágenes y contenedores; para recuperar datos persistentes se utiliza la restauración anterior. La retención temporal de Prometheus se controla con `PROMETHEUS_RETENTION` (15 días por defecto) y la de silencios/notificaciones de Alertmanager con `ALERTMANAGER_RETENTION` (120 horas).

Por defecto se comprueba la salud del proceso exporter sin convertir una caída externa de Informix en un rollback de software. Usa `-RequireInformix` o `--require-informix` cuando quieras exigir también una conexión y scrape correctos durante la actualización.

El dashboard versionado en `deploy/grafana/dashboards/informix-overview.json` permite seleccionar datasource e instancia. Agrupa las métricas por estado, actividad, buffers, almacenamiento, contención, checkpoints, consultas y ordenaciones.

El exporter publica snapshots correctos de gauges y contadores, todos los dbspaces y chunks existentes, su capacidad en bytes, estado e I/O. También expone sesiones y sus esperas, locks por tipo, transacciones y el uso/estado de cada logical log, además de telemetría propia (`informix_up` e `informix_exporter_scrape_*`). Los nombres de dbspaces y chunks se mantienen como etiquetas; las sesiones se agregan sin usuario, host ni identificador para controlar la cardinalidad.

### Métricas personalizadas

Se pueden añadir métricas escalares sin recompilar el exporter:

```yaml
custom:
  - name: application_queue_depth
    help: Registros pendientes en la cola de la aplicación.
    value_type: gauge
    query: select count(*) from application_queue where processed = 0
```

La serie resultante será `informix_custom_application_queue_depth{informixserver="..."}`. Cada consulta debe ser un único `SELECT` que devuelva exactamente una fila y una columna numérica. `value_type` admite `gauge` —valor predeterminado— o `counter`; los counters no pueden devolver valores negativos y su nombre debe terminar en `_total`. No se admiten etiquetas obtenidas de las filas, evitando que una consulta accidental genere cardinalidad ilimitada. Las consultas se ejecutan con el mismo usuario de solo lectura y dentro del timeout global del scrape.

Prometheus carga trece alertas versionadas desde `deploy/prometheus/rules/informix.yml`: caída de Alertmanager o Informix, fallo global o individual de colectores, lentitud del exporter, ocupación de dbspaces, chunks no disponibles, esperas por locks, transacciones largas, retraso en el backup de logical logs y proximidad al límite clásico de páginas.

## Desarrollo

El código está separado por responsabilidad:

```text
cmd/informix_exporter/   punto de entrada HTTP
internal/config/         carga y validación estricta del YAML
internal/informix/       conexiones ODBC y ciclo de vida de database/sql
internal/collector/      exporter y collectors de métricas
examples/                plantillas públicas sin credenciales
deploy/                  Docker Compose, Prometheus y Grafana
```

El prototipo histórico `tables/` se ha retirado. El collector actual de límites se conserva en `internal/collector/tables.go`, pero su evolución queda en pausa.

La integración continua definida en `.github/workflows/ci.yml` comprueba formato, tests con detector de carreras, `go vet`, vulnerabilidades conocidas mediante `govulncheck`, compilación, Docker Compose, reglas de Prometheus, JSON de Grafana y construcción de la imagen. Dependabot revisa semanalmente módulos Go, imágenes Docker, Compose y acciones de GitHub.

## Releases

El workflow `.github/workflows/release.yml` se ejecuta al publicar un tag estable con formato `vMAJOR.MINOR.PATCH`, por ejemplo:

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

La release genera:

- Un archivo `linux_amd64.tar.gz` con el binario, licencia, README y ejemplos.
- `checksums.txt` con SHA-256.
- Una GitHub Release con notas generadas.
- Las imágenes `ghcr.io/<propietario>/<repositorio>:<versión>` y `:latest`, con SBOM y procedencia de compilación.

El binario requiere unixODBC y el Client SDK instalado en la máquina. La imagen tampoco incorpora el Client SDK propietario: se sigue importando mediante el volumen descrito en esta guía.

## Pruebas de desarrollo

Las pruebas requieren Go, unixODBC y sus cabeceras. También pueden ejecutarse en Docker:

La dependencia `github.com/alexbrainman/odbc` está fijada a su revisión ANSI. El Client SDK incluido en versiones antiguas de Informix no maneja correctamente las llamadas `SQL*W` de la rama moderna del paquete; el síntoma es un error ilegible de `SQLDriverConnect` aunque `isql` conecte correctamente.

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.26-bookworm \
  sh -c "apt-get update >/dev/null && apt-get install -y --no-install-recommends unixodbc-dev >/dev/null && go test ./..."
```

## Licencia

El exporter se distribuye bajo GPL-3.0. Informix Client SDK y su driver ODBC tienen su propia licencia y no forman parte de este repositorio ni de la imagen generada por defecto.
