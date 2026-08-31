# EXORYN V0 — ESTADO DEL PROYECTO

**Commit:** `9db7c5e` · **Fecha:** 2026-08-31 · **Rama:** `master`, empujada a origin

Este documento dice qué es la plataforma, qué se construyó, qué se midió, qué se rompió y
se arregló, y qué queda abierto. Está escrito para que alguien que no siguió el trabajo
pueda decidir sobre él.

---

## 1. QUÉ ES

EXORYN es una **plataforma agéntica de aseguramiento de flujo de órdenes**: infraestructura
B2B para que instituciones financieras **atestigüen, autoricen, vigilen y contengan** el
flujo de órdenes generado por IA *antes de que llegue a la infraestructura de ejecución*.

No es un bot de trading. No genera ideas de inversión — hay un guardián en la suite que
falla si aparece un símbolo que sugiera lo contrario (ADR-001). V0 **no tiene camino a
dinero real**: los dos adaptadores de bróker rechazan cualquier endpoint que no sea de
papel.

Una frase que conviene tener clara porque cambia lo que se le puede prometer a un cliente:
**la plataforma nunca cancela una orden en el venue.** "Podemos pararlo" significa
*rechazamos la siguiente y revocamos la autoridad*, no *recuperamos la que ya está
trabajando*. Es coherente con el alcance declarado (§1, "antes de que llegue a la
infraestructura de ejecución") y ningún documento afirma lo contrario.

---

## 2. QUÉ HAY CONSTRUIDO

**Cuatro desplegables**, monorepo Go 1.25:

| Componente | Qué hace |
|---|---|
| `assurance-gateway` | El plano de aplicación. Recibe la envolvente del agente, evalúa autoridad y política, aplica controles, somete al venue y escribe evidencia. |
| `fleet-engine` | El plano de inteligencia. Mide cohortes por ventana, detecta incidentes, sirve la vista de flota. **Nunca decide una ejecución** (INV-009). |
| `console-web` | Seis superficies de lectura (Next.js 15). Sin camino de escritura: no autoriza, no somete, no cancela. |
| `simulator` | Gemelo digital: corridas reproducibles con huella de resultado y hash del escenario. |

**Infraestructura:** PostgreSQL como verdad (con RLS y FORCE RLS), ClickHouse analítico,
NATS JetStream como bus, MinIO como almacén de objetos, SPIRE/SPIFFE para identidad de
carga de trabajo.

**Tamaño medido:**

```
23.139  líneas de código de producción (Go)
33.107  líneas de prueba (Go)
 1.413  líneas de consola (TypeScript/React)
   675  funciones de prueba
    64  migraciones de PostgreSQL · 9 de ClickHouse
    29  ADRs
   136  commits
```

La proporción prueba/producción es 1,43 a 1. Eso no es una virtud por sí solo — buena parte
de este documento trata de pruebas que medían la cosa equivocada.

---

## 3. LAS PROPIEDADES QUE SOSTIENE

15 invariantes (INV-001…INV-015), 10 principios (P-001…P-010), cuatro niveles de
atestación (A0–A3). Las que más peso cargan:

- **Dinero exacto, nunca `float64` en el camino de decisión.** `money.Amount` (escala 4) y
  `money.Quantity` (escala 8) se serializan como texto decimal literal y se parsean del
  literal. El plano analítico sí usa flotantes, a propósito y en un solo sentido.
- **El inquilino sale de la credencial, jamás de la petición** (INV-007, ADR-025). Una
  cabecera que nombre otro inquilino da 403.
- **Idempotencia permanente**: una clave retirada no revive; los tombstones sobreviven a la
  poda.
- **Evidencia con cadena de hashes**, canonicalizada con literales numéricos verbatim, y un
  outbox transaccional con arriendo (`FOR UPDATE SKIP LOCKED`).
- **Falla cerrado**: sin política, deniega; sin base, deniega; la caída de ClickHouse,
  NATS, Redis o del plano de inteligencia **no** afecta la ejecución.
- **La consola no es requisito para ejecutar** (§59, §17).

---

## 4. LAS CATORCE AUDITORÍAS

Cada pasada usó un método distinto, a propósito: repetir el método sólo vuelve a encontrar
lo que ese método ya sabe mirar.

| # | Método | Hallazgo principal |
|---|---|---|
| 1–4 | Remediaciones sobre auditorías externas | — |
| 5 | Auto-revisión de la cuarta remediación | **CRÍTICO**: la evidencia administrativa nunca llegaba al bus · el outbox sin retención: 4,3 GB de una base de 8,3 |
| 6 | Reconciliación: leer la evidencia, no el código | **ALTO**: el camino de liberación nunca había funcionado, y la plataforma lo venía diciendo |
| 7 | Barrido de mutación sobre puntos elegidos | Qué dicen las pruebas cuando el código miente |
| 8 | Una mutación por invariante de la especificación | **MEDIO ×3**: INV-010 (un bundle no ACTIVE podía aplicar), INV-014, INV-013 |
| 9 | Censo de rechazos por cobertura de ejecución | 28 códigos de rechazo que ninguna prueba había producido jamás |
| 10 | Censo de bordes: cada comparación de dinero y tiempo | **MEDIO**: tres copias de una regla, dos correctas |
| 11 | Censo de superficie exportada (337 símbolos) | 6 funciones sin ningún llamador — **dos las había escrito yo** en auditorías previas |
| 12 | El mismo censo sobre `adapters/` y `cmd/` | **ALTO**: el adaptador de Alpaca descartaba errores de parseo en el camino de ejecución |
| 13 | La consola: tipos declarados vs. lo que el backend serializa | **MEDIO**: los conteos llegaban como cadena; `"9" > "10"` |
| 14 | Las promesas en prosa, tomadas como aserciones | **ALTO**: la contraseña de ClickHouse en la URL, y por tanto en tres logs |

**Total: 1 crítico, 4 altos, 6 medios, 1 bajo, más los censos.**

### El patrón, que es el resultado más útil de las catorce

**Todo método que arranca de un documento escrito encuentra sólo lo que ese documento
menciona. Los métodos que arrancan del sistema corriendo o del código encuentran lo que los
documentos no saben.**

Y el segundo, que costó más admitir: **el instrumento mintió en casi todas las pasadas.** El
censo de superficie reportó "138 de 138 sin llamador" (una alternación de regex que no
casaba con nada); el arnés de mutación reportó un demonio de Docker muerto como 23 "no
compila"; el sondeo de la décima estaba mal y aun así encontró algo. Un resultado unánime
significa que el instrumento está roto, no el código. Cada reporte lo dice de sí mismo.

---

## 5. LA MATRIZ DE VALIDACIÓN — CORRIDA, POR FIN

Siete auditorías la debieron. Docker estuvo apagado y levantarlo requería elevación. Corrió
completa el 2026-08-31:

```
integración          122 pasan ·  3 saltan · 0 fallan
race                 limpio, cero carreras de datos
race + integración   limpio            ← aquí está la concurrencia real
chaos                  9 de 9
proceso                2 de 3
compuerta (verify)   verde, consola incluida (lint, typecheck, next build)
```

**Ningún defecto de producción.** El sistema sobrevive a la caída de ClickHouse, NATS, Redis
y del plano de inteligencia; falla cerrado cuando cae Postgres; dos procesos de gateway no
pueden sobregirar una autorización ni bifurcar la historia de políticas; la evidencia
sobrevive a un corte del bus.

**Lo que la matriz sí atrapó fueron dos instrumentos**, y uno era mío:

1. `fleet_e2e` casaba el substring `"c":"3"` — el conteo **con comillas**, o sea el defecto
   de A-13-01 codificado como expectativa. Al corregir el entrecomillado, la prueba sondeó
   cinco segundos y reportó *"12 intents never became visible"*. Habían llegado en el primer
   sondeo. Ahora decodifica la fila.
2. El guardián de credenciales decía *"committed env file"* y medía **presencia en disco**,
   así que fallaba con el `.env` que uno crea copiando `.env.example` — el flujo documentado,
   y sin el cual la integración no corre. Ahora le pregunta a git qué está versionado.

Ninguno era un defecto de producción; los dos habrían seguido mintiendo en CI.

### Lo que no corrió, y por qué

- **Proceso 2 de 3.** `TestAKilledGatewayDoesNotResubmit` no arranca: Smart App Control de
  Windows bloquea ese binario en este host. Probado cuatro veces, por ruta estable y por
  PowerShell. **No es el código.**
- **3 saltos en integración**: dos de campos de consola contra un gateway vivo en `:8080`
  (no había uno corriendo) y dos de Alpha Vantage en vivo (sin clave en el entorno).
- Hacia el final SAC empezó a bloquear binarios recién compilados de forma sostenida en
  Windows. El árbol completo se verificó **en el contenedor Linux**, incluidos los paquetes
  exactos que bloqueaba, y ahí está todo verde.

---

## 6. DÓNDE ESTAMOS

**V0 no está auto-aceptado, y esa sigue siendo la postura correcta.** Pero el estado cambió
de forma material con esta corrida: por primera vez en todo el registro la plataforma se
midió entera, y aguantó.

Lo que falta no son defectos conocidos. Son tres decisiones de producto y una tarea de
operación.

### Acción de operación — tuya, urgente

**Rotar la contraseña de ClickHouse.** El arreglo de A-14-01 detiene fugas nuevas; no borra
las viejas. Si esto corrió en algún despliegue real, la contraseña ya está escrita en los
logs de ese despliegue, y posiblemente en el access log de un proxy inverso y en el
`system.query_log` de la propia ClickHouse.

### Tres decisiones de producto — tuyas, no son bugs

1. **Retención sin punto de entrada para el operador.** `retention.Verify` y
   `Exporter.Restore` existen y funcionan; no hay endpoint, ni comando, ni agendador que
   los invoque. La consecuencia concreta: `archive_manifests.verified_at` es `NULL` para
   todo manifiesto que llegue a existir, así que la plataforma no puede responder *"cuándo
   se probó por última vez que este archivo es el que el manifiesto describe"*. Para un
   producto cuya propuesta entera es un registro sobre el que alguien puede actuar, esa es
   la columna equivocada para no haber escrito nunca. **No lo cableé a propósito**:
   inventarle un llamador produciría exactamente lo que la undécima auditoría borró.
2. **`broker.Adapter` es cinco métodos más ancha de lo que la plataforma usa.** Exige siete,
   se llaman dos. Cada integración de venue implementa y despliega cinco caminos de API en
   vivo sin llamador — que es exactamente la superficie donde vivía A-12-01. O se estrecha
   el contrato, o se escribe por qué se mantiene.
3. **La consola no tiene ni una prueba de comportamiento.** Las seis páginas sólo las
   ejercita `next build`. Nada afirma que una fuente inalcanzable renderice `Unavailable` en
   vez de ceros — que es la promesa más importante que hace la consola. Hoy las seis lo
   hacen bien, verificado leyendo.

### Riesgos abiertos que arrastramos

- **360 de las 366 afirmaciones absolutas del código siguen sin examinar.** La decimocuarta
  revisó una clase porque era la de mayor apuesta, no porque las otras estén a salvo.
- **Nada impide la clase de A-14-01 hacia adelante.** Dos pruebas cuidan el cliente de
  ClickHouse en particular; ningún chequeo impide que la próxima credencial vaya a una URL.
- **`authority_usage` crece sin límite** y no hay endpoint que liste las claves registradas.
- **`money.Amount.Add` es suma `int64` sin chequeo** en el extremo: dos valores en el máximo
  del parser suman uno más que `MaxInt64`. Requiere ~461 billones ya consumidos; es un borde
  teórico, escrito para que no se asuma.
- **Presupuesto de conexiones sin documentar** — se llegó a 92 de 100.
- **El lado de lectura de Tradier y el almacén de objetos** siguen sin ejecutarse.
- **Smart App Control bloquea binarios recién compilados en este host**, de forma
  intermitente y a veces sostenida. Es la causa de que un caso de la suite de proceso no
  corra y de que verificar un "rojo" a veces sea imposible en Windows. El contenedor Linux
  es el camino confiable.

---

## 7. CÓMO SE CORRE

```bash
docker compose up -d --wait
sh scripts/migrate.sh
cp .env.example .env    # los valores locales terminan en _dev_only
```

```bash
set -a; . ./.env; set +a
go test -tags=integration -count=1 ./tests/integration/...
sh scripts/test-race.sh                 # el detector, en contenedor
INTEGRATION=1 sh scripts/test-race.sh   # con la suite de integración
go test -tags=chaos -count=1 ./tests/chaos/...     # apaga contenedores: corre solo
go test -tags=process -count=1 ./tests/process/...
bash scripts/verify.sh                  # la compuerta
```

`.env` está en `.gitignore` y el guardián lo permite explícitamente desde `9db7c5e`.

**Ninguna credencial real vive en un archivo del repositorio.** Las de Alpaca de papel y la
de Alpha Vantage se pasan por entorno cuando se necesitan; una prueba falla si aparecen en
el árbol, y los valores de prueba deliberados terminan en `_dev_only`.

---

## 8. DOCUMENTOS

Los catorce reportes están en `docs/`, uno por auditoría, cada uno con su método, sus
hallazgos, lo que corrió, lo que no corrió y por qué, y los riesgos que quedaron abiertos:

```
V0_AUDIT_REMEDIATION_REPORT.md              V0_NINTH_AUDIT_REFUSAL_CENSUS.md
V0_SECOND_AUDIT_REMEDIATION_REPORT.md       V0_TENTH_AUDIT_BOUNDARIES.md
V0_THIRD_AUDIT_REMEDIATION_REPORT.md        V0_ELEVENTH_AUDIT_SURFACE.md
V0_FOURTH_AUDIT_REMEDIATION_REPORT.md       V0_TWELFTH_AUDIT_EDGES.md
V0_FIFTH_AUDIT_SELF_REVIEW.md               V0_THIRTEENTH_AUDIT_CONSOLE.md
V0_SIXTH_AUDIT_RECONCILIATION.md            V0_FOURTEENTH_AUDIT_SECRETS.md
V0_SEVENTH_AUDIT_MUTATION_SWEEP.md          V0_EIGHTH_AUDIT_INVARIANT_CENSUS.md
```

Más 29 ADRs en `docs/adr/` y las herramientas de auditoría en `tools/`: `mutation/sweep.py`,
`refusals/census.py`, `surface/census.py`.

---

## 9. UNA NOTA SOBRE EL PROCESO

Catorce auditorías es mucho, y la razón honesta es que se pidieron una por una y yo nunca
dije *basta*. Eso era mío decirlo.

Dicho eso, el ritmo de hallazgos no se cayó: la 12, la 13 y la 14 encontraron defectos
reales, y el último era una fuga de credencial en el camino de fallo normal. Lo que sí se
torció fue la forma — durante siete pasadas hice el trabajo que **podía** hacer sin
infraestructura en lugar del que **hacía falta**. Auditar era trabajo disponible; correr la
matriz era trabajo necesario. Sustituir el segundo por el primero se ve exactamente igual
que avanzar, y no lo es.

Esa deuda quedó saldada hoy.
