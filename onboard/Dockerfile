FROM node:22-alpine
WORKDIR /app
COPY onboard/package.json onboard/package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund \
    && mkdir -p /licenses /binding/private /binding/public \
    && node --input-type=module -e 'import {writeFile} from "node:fs/promises"; const r = await fetch("https://raw.githubusercontent.com/nodejs/node/" + process.version + "/LICENSE"); if (!r.ok) throw Error("Node license download failed"); await writeFile("/licenses/NODE-LICENSE", await r.text())' \
    && chown -R 10001:10001 /binding \
    && chmod 700 /binding/private /binding/public
COPY onboard/index.mjs onboard/login.mjs ./
COPY LICENSE THIRD_PARTY_NOTICES.md /licenses/
COPY third_party/licenses/npm /licenses/npm/
USER 10001:10001
ENTRYPOINT ["node", "/app/index.mjs"]
