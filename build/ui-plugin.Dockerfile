# Builds one of the portal plugins (ui/consumer for cloud-portal, ui/provider
# for staff-portal) and serves its dist/ with ui/serve.ts over HTTPS. The
# portals' asset proxies are the only clients; see ui/serve.ts.
#
# The context is the repository root because both plugins compile
# ../shared. ui-plugin.Dockerfile.dockerignore admits only ui/.
ARG PLUGIN

FROM node:26.13.0-alpine3.24@sha256:8996bb5821d62a8fd9bb27c1cb07462f3fdce6e47d640c6a881ead0b064ce216 AS build
ARG PLUGIN
RUN test "$PLUGIN" = consumer || test "$PLUGIN" = provider
WORKDIR /src/ui/${PLUGIN}
# Node 26 ships no corepack, so install the pnpm that package.json's
# packageManager pins; it stays in this stage only.
COPY ui/${PLUGIN}/package.json ui/${PLUGIN}/pnpm-lock.yaml ui/${PLUGIN}/pnpm-workspace.yaml ./
RUN npm install -g "$(node -p 'require("./package.json").packageManager')" --no-audit --no-fund && \
  pnpm install --frozen-lockfile --ignore-scripts
COPY ui/shared/ /src/ui/shared/
COPY ui/${PLUGIN}/ ./
RUN pnpm build

FROM node:26.13.0-alpine3.24@sha256:8996bb5821d62a8fd9bb27c1cb07462f3fdce6e47d640c6a881ead0b064ce216
ARG PLUGIN
# Node keeps its own copy of the CA roots apart from Alpine's bundle. Fail the
# build if either still trusts the compromised eMudhra root (emSign ECC Root
# CA - G3).
RUN apk add --no-cache --upgrade 'ca-certificates>=20261020-r0'
RUN ! grep -q MIICTjCCAdOgAwIBAgIKPPYHqWhwDtqLhDAKBggqhkjOPQQDAzBrMQswCQYDVQQG /etc/ssl/certs/ca-certificates.crt
RUN node -e 'const { rootCertificates } = require("node:tls"); const { X509Certificate } = require("node:crypto"); const compromised = rootCertificates.some((pem) => new X509Certificate(pem).subject.split("\n").includes("CN=emSign ECC Root CA - G3")); if (compromised) { console.error("refusing to build: Node trusts emSign ECC Root CA - G3"); process.exit(1); }'
WORKDIR /app
COPY --from=build /src/ui/${PLUGIN}/dist ./dist
COPY ui/serve.ts ./
# uid/gid 1000, as the Deployment's securityContext expects.
USER 1000:1000
EXPOSE 8443
CMD ["node", "serve.ts"]
