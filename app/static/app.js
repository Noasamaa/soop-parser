(() => {
  const $ = (id) => document.getElementById(id);

  const urlInput = $("urlInput");
  const pwdInput = $("pwdInput");
  const tokenInput = $("tokenInput");
  const proxyToggle = $("proxyToggle");
  const resolveBtn = $("resolveBtn");
  const errorCard = $("errorCard");
  const errorText = $("errorText");
  const resultCard = $("resultCard");
  const titleText = $("titleText");
  const platformTag = $("platformTag");
  const liveTag = $("liveTag");
  const authorTag = $("authorTag");
  const channelTag = $("channelTag");
  const bnoTag = $("bnoTag");
  const qualitiesEl = $("qualities");
  const player = $("player");
  const playHint = $("playHint");
  const configMeta = $("configMeta");
  const copyRow = $("copyRow");
  const playUrlOut = $("playUrlOut");
  const copyBtn = $("copyBtn");

  let hls = null;

  const TOKEN_KEY = "soop_parser_access_token";
  const saved = localStorage.getItem(TOKEN_KEY);
  if (saved) tokenInput.value = saved;

  tokenInput.addEventListener("change", () => {
    const v = tokenInput.value.trim();
    if (v) localStorage.setItem(TOKEN_KEY, v);
    else localStorage.removeItem(TOKEN_KEY);
  });

  function authHeaders() {
    const headers = { "Content-Type": "application/json" };
    const t = tokenInput.value.trim();
    if (t) headers["X-Access-Token"] = t;
    return headers;
  }

  function withToken(url) {
    const t = tokenInput.value.trim();
    if (!t) return url;
    const sep = url.includes("?") ? "&" : "?";
    return `${url}${sep}token=${encodeURIComponent(t)}`;
  }

  function showError(msg) {
    errorText.textContent = msg;
    errorCard.classList.remove("hidden");
  }

  function clearError() {
    errorCard.classList.add("hidden");
    errorText.textContent = "";
  }

  function destroyPlayer() {
    if (hls) {
      hls.destroy();
      hls = null;
    }
    player.removeAttribute("src");
    player.load();
  }

  function isHlsUrl(url, protocol) {
    if (protocol === "progressive") return false;
    if (protocol === "hls") return true;
    return url.includes(".m3u8") || url.includes("/playlist.m3u8") || url.includes("/api/hls/");
  }

  function setPlayUrlOutput(url) {
    if (!url) {
      copyRow.classList.add("hidden");
      playUrlOut.value = "";
      return;
    }
    copyRow.classList.remove("hidden");
    playUrlOut.value = url;
  }

  function playUrl(url, protocol) {
    destroyPlayer();
    // play_url from API is already absolute + may already include ?token=
    let finalUrl = url;
    if (!/^https?:\/\//i.test(finalUrl) && !finalUrl.startsWith("/")) {
      finalUrl = url;
    }
    // relative fallback for old responses
    if (finalUrl.startsWith("/")) {
      finalUrl = `${location.origin}${finalUrl}`;
    }
    if (!/[?&]token=/.test(finalUrl)) {
      finalUrl = withToken(finalUrl);
    }
    setPlayUrlOutput(finalUrl);
    playHint.textContent = `播放中（经本站代理）：${finalUrl}`;

    if (!isHlsUrl(finalUrl, protocol)) {
      // Progressive mp4/webm
      player.src = finalUrl;
      player.play().catch(() => {});
      return;
    }

    if (player.canPlayType("application/vnd.apple.mpegurl")) {
      player.src = finalUrl;
      player.play().catch(() => {});
      return;
    }

    if (window.Hls && Hls.isSupported()) {
      hls = new Hls({
        enableWorker: true,
        lowLatencyMode: true,
        xhrSetup: (xhr) => {
          const t = tokenInput.value.trim();
          if (t) xhr.setRequestHeader("X-Access-Token", t);
        },
      });
      hls.loadSource(finalUrl);
      hls.attachMedia(player);
      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        player.play().catch(() => {});
      });
      hls.on(Hls.Events.ERROR, (_, data) => {
        if (data.fatal) {
          showError(`播放失败：${data.type} / ${data.details || "unknown"}`);
        }
      });
      return;
    }

    showError("当前浏览器不支持 HLS 播放，请使用 Chrome / Edge / Safari，或把地址复制到 VLC。");
  }

  function renderResult(data) {
    resultCard.classList.remove("hidden");
    titleText.textContent = data.title || "(无标题)";
    platformTag.textContent = (data.platform || "unknown").toUpperCase();
    liveTag.textContent = data.is_live ? "LIVE" : "VOD";
    authorTag.textContent = data.author ? `频道 ${data.author}` : "频道 —";
    channelTag.textContent = data.channel ? `ID ${data.channel}` : "";
    bnoTag.textContent = data.bno ? `视频 ${data.bno}` : "";

    qualitiesEl.innerHTML = "";
    const currentQualities = data.qualities || [];

    if (!currentQualities.length) {
      playHint.textContent = "没有可用清晰度";
      return;
    }

    currentQualities.forEach((q, idx) => {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "ghost";
      btn.textContent = q.label || q.name;
      btn.addEventListener("click", () => {
        [...qualitiesEl.querySelectorAll("button")].forEach((b) => b.classList.remove("active"));
        btn.classList.add("active");
        const url = q.play_url || q.direct_url;
        if (!url) {
          showError("该清晰度没有可播放地址");
          return;
        }
        clearError();
        playUrl(url, q.protocol || "hls");
      });
      qualitiesEl.appendChild(btn);
      if (idx === 0) btn.click();
    });
  }

  async function loadConfig() {
    try {
      const res = await fetch(withToken("/api/config"), { headers: authHeaders() });
      if (res.status === 401) {
        configMeta.textContent = "需要访问令牌才能使用";
        return;
      }
      const data = await res.json();
      const parts = [];
      parts.push((data.platforms || []).join(" + ") || "soop");
      if (data.public_base_url) parts.push(`公网: ${data.public_base_url}`);
      parts.push(data.auth_required ? "已启用访问令牌" : "未启用访问令牌");
      parts.push(data.login_configured ? "SOOP 已登录配置" : "SOOP 未配置登录");
      parts.push(data.youtube_cookies_configured ? "YT cookies 已配置" : "YT cookies 未配置");
      parts.push(`会话 ${data.play_token_ttl}s`);
      configMeta.innerHTML = parts.join("<br/>");
    } catch {
      configMeta.textContent = "配置加载失败（服务可能未启动）";
    }
  }

  async function resolve() {
    clearError();
    const url = urlInput.value.trim();
    if (!url) {
      showError("请输入 SOOP 或 YouTube 链接");
      return;
    }

    resolveBtn.disabled = true;
    resolveBtn.textContent = "解析中…";
    resultCard.classList.add("hidden");
    destroyPlayer();

    try {
      const res = await fetch("/api/resolve", {
        method: "POST",
        headers: authHeaders(),
        body: JSON.stringify({
          url,
          stream_password: pwdInput.value,
          proxy: proxyToggle.checked,
        }),
      });

      const data = await res.json().catch(() => ({}));
      if (!res.ok || data.ok === false) {
        const msg = data.message || data.detail || `HTTP ${res.status}`;
        const code = data.code ? `[${data.code}] ` : "";
        showError(`${code}${typeof msg === "string" ? msg : JSON.stringify(msg)}`);
        return;
      }

      renderResult(data);
    } catch (err) {
      showError(`网络错误：${err.message || err}`);
    } finally {
      resolveBtn.disabled = false;
      resolveBtn.textContent = "解析";
    }
  }

  resolveBtn.addEventListener("click", resolve);
  urlInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") resolve();
  });
  copyBtn.addEventListener("click", async () => {
    const v = playUrlOut.value;
    if (!v) return;
    try {
      await navigator.clipboard.writeText(v);
      copyBtn.textContent = "已复制";
      setTimeout(() => {
        copyBtn.textContent = "复制";
      }, 1200);
    } catch {
      playUrlOut.select();
      showError("复制失败，请手动全选输入框");
    }
  });

  const params = new URLSearchParams(location.search);
  if (params.get("url")) urlInput.value = params.get("url");
  if (params.get("token")) {
    tokenInput.value = params.get("token");
    localStorage.setItem(TOKEN_KEY, params.get("token"));
  }

  loadConfig();
})();
