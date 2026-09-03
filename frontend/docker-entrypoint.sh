#!/bin/sh

# Only emit whitelisted locale tags to avoid config.js injection from env values.
RUNTIME_DEFAULT_LOCALE=""
case "${DEFAULT_LOCALE:-}" in
  zh-CN|en-US|ru-RU|ko-KR) RUNTIME_DEFAULT_LOCALE="${DEFAULT_LOCALE}" ;;
esac

# 生成运行时配置文件，注入环境变量到前端
FILE_MB=${MAX_FILE_SIZE_MB:-50}
SKILL_MB=${MAX_SKILL_BUNDLE_SIZE_MB:-256}
if [ "$SKILL_MB" -lt "$FILE_MB" ] 2>/dev/null; then
  SKILL_MB=$FILE_MB
fi
if [ "$SKILL_MB" -gt 512 ] 2>/dev/null; then
  SKILL_MB=512
fi

cat > /usr/share/nginx/html/config.js << EOF
window.__RUNTIME_CONFIG__ = {
  MAX_FILE_SIZE_MB: ${FILE_MB},
  MAX_SKILL_BUNDLE_SIZE_MB: ${SKILL_MB},
  DEFAULT_LOCALE: "${RUNTIME_DEFAULT_LOCALE}"
};
EOF

# 处理 nginx 配置。
# 两个上限分开注入：全站保持知识库的 MAX_FILE_SIZE，只有技能 zip 上传的两条
# 集合路由放宽到 MAX_SKILL_BUNDLE_SIZE（不含 /install、PATCH 等子路径）。
# 合成一个全站上限会让每个上传端点都能收到技能包那么大的 body。
export MAX_FILE_SIZE=${FILE_MB}M
export MAX_SKILL_BUNDLE_SIZE=${SKILL_MB}M
export APP_HOST=${APP_HOST:-app}
export APP_PORT=${APP_PORT:-8080}
export APP_SCHEME=${APP_SCHEME:-http}
# FastGPT 同步桥接地址（nginx location /fastgpt/ 的反代目标）。必须给默认值：
# 若留空，envsubst 后 nginx.conf 会残留字面量 ${SYNC_BRIDGE_URL}，nginx 启动即报 unknown variable 崩溃。
export SYNC_BRIDGE_URL=${SYNC_BRIDGE_URL:-http://127.0.0.1:8000}
# 必须合并成一条：两条 envsubst 输入输出相同、都用 > 覆盖写，后一条会整体丢弃前一条。
# 若拆成两条且各自白名单不全，真正生效的那条一旦漏掉某个变量（如 SYNC_BRIDGE_URL），
# nginx.conf 里对应的 ${...} 就会残留字面量，nginx 启动即报 unknown variable 崩溃。
# 白名单取并集：MAX_FILE_SIZE / MAX_SKILL_BUNDLE_SIZE / APP_HOST / APP_PORT / APP_SCHEME / SYNC_BRIDGE_URL。
envsubst '${MAX_FILE_SIZE} ${MAX_SKILL_BUNDLE_SIZE} ${APP_HOST} ${APP_PORT} ${APP_SCHEME} ${SYNC_BRIDGE_URL}' < /etc/nginx/templates/default.conf.template > /etc/nginx/conf.d/default.conf

# 启动 nginx
exec nginx -g 'daemon off;'
