# Política de seguridad

`quantum-app` es una **aplicación de referencia**: existe para ejercitar de
punta a punta el set certificado de la suite [Quantum](https://github.com/jcsvwinston/quantum),
no para que nadie la importe como librería ni la despliegue. Por eso casi
cualquier vulnerabilidad que se vea aquí vive en realidad **aguas arriba**, en
uno de los tres productos.

## Dónde reportar

**No abras un issue público.** Elige el destino por dónde esté el fallo:

| El fallo está en… | Reporta en |
|---|---|
| Nucleus (framework web) | [nucleus/SECURITY.md](https://github.com/jcsvwinston/nucleus/blob/main/SECURITY.md) |
| Quark (ORM) | [quark/SECURITY.md](https://github.com/jcsvwinston/quark/blob/main/SECURITY.md) |
| Orbit (panel de administración) | [orbit/SECURITY.md](https://github.com/jcsvwinston/orbit/blob/main/SECURITY.md) |
| el código de ESTA app (configuración de ejemplo, scripts, E2E, workflows) | [advisory privado de quantum-app](https://github.com/jcsvwinston/quantum-app/security/advisories/new) |
| no lo sabes, o solo aparece con los tres juntos | el paraguas: [quantum/SECURITY.md](https://github.com/jcsvwinston/quantum/blob/main/SECURITY.md) |

El reporte privado no está habilitado en todos los repos, así que el canal que
siempre funciona es el correo: **serrano.juan.carlos@gmail.com**. Si el
formulario de advisory de la tabla no te deja abrir nada, usa ese correo — no
es un rechazo, es una casilla sin activar.

Incluye el set contra el que lo viste (el número `Quantum X.Y.Z` que nombra la
cabecera de `go.mod`), cómo reproducirlo y el impacto que le atribuyes.

## Qué versión se mantiene

Solo `main`, y solo contra el set certificado que `go.mod` pina en ese momento.
Aquí no hay ramas de soporte ni parches para sets antiguos: la corrección de un
fallo aguas arriba llega a este repo cuando el paraguas certifica un set nuevo
y [`.github/workflows/set-bump.yml`](.github/workflows/set-bump.yml) mueve el
pin (nunca a versiones intermedias). Si necesitas la corrección antes, súbela
en TU aplicación: este repo no es una dependencia tuya.

El barrido de vulnerabilidades de las dependencias de la suite lo hace
`govulncheck` en el CI de cada producto; el CI de aquí compila, pasa `go vet`,
los tests y los gates del set, y la E2E real contra servicios en Docker.

## Qué no es una vulnerabilidad de este repo

- **Las credenciales de ejemplo** de `docker-compose.yml`, `config/e2e.yaml` y
  los scripts de E2E. Son valores de laboratorio declarados como tales, y los
  dos **secretos de despliegue** —`WAREHOUSE_OUTBOX_SECRET` y
  `WAREHOUSE_OPS_PASSWORD`— arrancan *fail-closed*: la app se niega a levantar
  si están vacíos o siguen en un placeholder `dev-` (ver «Deployment secrets»
  en el [README](README.md)).
- **Los servicios que `docker compose` levanta** sin TLS y con contraseñas
  triviales: son el entorno local de la E2E, no un despliegue.

El *fail-closed* cubre esos dos secretos, no toda credencial del árbol. Hay
una **excepción conocida, y un reporte sobre ella es válido**:
`WAREHOUSE_ADMIN_PASSWORD`, la contraseña de bootstrap del panel de Orbit, la
lee `cmd/quantum-app/main.go` con `envOr(..., "warehouse-admin")` — o sea,
tiene un default cableado y la app arranca con él si nadie define la variable.
Está documentada como pendiente en el [README](README.md) y no la cierra este
documento: si la reportas, se acepta.

Si crees que uno de los dos casos de arriba sí es explotable tal y como está
escrito —por ejemplo porque el fail-closed tiene un hueco—, entonces también
es un fallo de este repo: repórtalo por el canal de arriba.
