(() => {
  const $ = (id) => document.getElementById(id);

  const urlInput = $("urlInput");
  const pwdInput = $("pwdInput");
  const tokenInput = $("tokenInput");
  const tokenWrap = $("tokenWrap");
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
  let authRequired = false;

  // 台湾英雄联盟中文解说常用源（频道页，开播时自动取当前场次）
  const DEFAULT_PRESETS = [
    {
      id: "loltw",
      label: "台灣中文",
      badge: "SOOP",
      url: "https://play.sooplive.com/loltw",
      primary: true,
    },
    {
      id: "lckcarry",
      label: "LCK 中文",
      badge: "SOOP",
      url: "https://play.sooplive.com/lckcarry",
    },
    {
      id: "carrylck",
      label: "LCK 中文·備",
      badge: "SOOP",
      url: "https://play.sooplive.com/carrylck",
    },
    {
      id: "yt-lckcarry",
      label: "LCK-Carry",
      badge: "YT",
      url: "https://www.youtube.com/@LCKCarry/live",
    },
    {
      id: "yt-lcp",
      label: "LCP / 太平洋",
      badge: "YT",
      url: "https://www.youtube.com/@lolesportstw/live",
    },
  ];

  const TOKEN_KEY = "live_parser_access_token";
  const PRESET_KEY = "live_parser_last_preset";
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
    if (!authRequired) return url;
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

  function looksLikeCNPlatform(url) {
    const u = (url || "").toLowerCase().trim();
    return u.includes("bilibili.com") || u.includes("huya.com") || /^\d{1,12}$/.test(u);
  }

  /** Proxy flag for API: CN always direct; SOOP/YT use checkbox (default on). */
  function resolveProxyFlag(url) {
    if (looksLikeCNPlatform(url)) return false;
    return proxyToggle.checked;
  }

  function playUrl(url, protocol) {
    destroyPlayer();
    let finalUrl = url;
    if (finalUrl.startsWith("/")) {
      finalUrl = `${location.origin}${finalUrl}`;
    }
    if (authRequired && !/[?&]token=/.test(finalUrl)) {
      finalUrl = withToken(finalUrl);
    }
    setPlayUrlOutput(finalUrl);
    const viaProxy = finalUrl.includes("/api/hls/") || finalUrl.includes("/api/media/");
    const isFlv = protocol === "progressive" || /\.flv(\?|$)/i.test(finalUrl);
    if (viaProxy) {
      playHint.textContent = `播放中（经本站代理）：${finalUrl}`;
    } else if (isFlv) {
      playHint.textContent = `直连 FLV（浏览器通常无法播）：可复制到 PotPlayer / VLC。${finalUrl}`;
    } else {
      playHint.textContent = `播放中（直连 CDN）：${finalUrl}`;
    }

    if (!isHlsUrl(finalUrl, protocol)) {
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
          if (!authRequired) return;
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

    showError("当前浏览器不支持 HLS 播放，请使用 Chrome / Edge / Safari，或把地址复制到 VLC / PotPlayer。");
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
    const list = data.qualities || [];
    if (!list.length) {
      playHint.textContent = "没有可用清晰度";
      return;
    }

    list.forEach((q, idx) => {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "ghost";
      btn.textContent = q.label || q.name;
      btn.addEventListener("click", () => {
        [...qualitiesEl.querySelectorAll("button")].forEach((b) => b.classList.remove("active"));
        btn.classList.add("active");
        const u = q.play_url || q.direct_url;
        if (!u) {
          showError("该清晰度没有可播放地址");
          return;
        }
        clearError();
        playUrl(u, q.protocol || "hls");
      });
      qualitiesEl.appendChild(btn);
      if (idx === 0) btn.click();
    });
  }

  async function loadConfig() {
    try {
      const res = await fetch(withToken("/api/config"), { headers: authHeaders() });
      if (res.status === 401) {
        authRequired = true;
        tokenWrap.classList.remove("hidden");
        configMeta.textContent = "需要访问令牌才能使用";
        return;
      }
      const data = await res.json();
      authRequired = !!data.auth_required;
      if (authRequired) tokenWrap.classList.remove("hidden");
      else tokenWrap.classList.add("hidden");

      if (Array.isArray(data.presets) && data.presets.length) {
        renderPresets(data.presets);
        // re-apply active highlight after server presets replace DOM
        const cur = urlInput.value.trim();
        const hit = data.presets.find((p) => p.url === cur);
        if (hit) setPresetActive(hit.id);
      }

      const parts = [];
      parts.push((data.platforms || []).join(" + ") || "soop");
      if (data.engine) parts.push(`引擎 ${data.engine}`);
      if (data.public_base_url) parts.push(`公网: ${data.public_base_url}`);
      parts.push(authRequired ? "已启用访问令牌" : "未启用访问令牌");
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
      showError("请输入 SOOP / YouTube / B站 / 虎牙 链接");
      return;
    }

    // 展示与后端一致：识别到 B站/虎牙时关代理勾选（不写 userTouched，避免污染 SOOP/YT）
    if (looksLikeCNPlatform(url)) {
      proxyToggle.checked = false;
    }
    const proxy = resolveProxyFlag(url);

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
          proxy,
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

  function setPresetActive(id) {
    document.querySelectorAll(".preset-btn").forEach((b) => {
      b.classList.toggle("active", b.dataset.id === id);
    });
  }

  function renderPresets(list) {
    const el = $("presets");
    if (!el) return;
    el.innerHTML = "";
    list.forEach((p) => {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "preset-btn";
      btn.dataset.id = p.id;
      btn.dataset.url = p.url;
      btn.innerHTML = `${p.label}<span class="badge">${p.badge || ""}</span>`;
      btn.title = p.url;
      btn.addEventListener("click", () => {
        urlInput.value = p.url;
        setPresetActive(p.id);
        localStorage.setItem(PRESET_KEY, p.id);
        resolve();
      });
      el.appendChild(btn);
    });
  }

  resolveBtn.addEventListener("click", resolve);
  urlInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") resolve();
  });
  urlInput.addEventListener("input", () => {
    if (looksLikeCNPlatform(urlInput.value)) {
      proxyToggle.checked = false;
    }
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
  if (params.get("token")) {
    tokenInput.value = params.get("token");
    localStorage.setItem(TOKEN_KEY, params.get("token"));
  }

  renderPresets(DEFAULT_PRESETS);

  // 默认：URL 参数 > 上次预设 > 台灣中文 loltw
  const primary = DEFAULT_PRESETS.find((p) => p.primary) || DEFAULT_PRESETS[0];
  const lastId = localStorage.getItem(PRESET_KEY);
  const last = DEFAULT_PRESETS.find((p) => p.id === lastId);
  if (params.get("url")) {
    urlInput.value = params.get("url");
  } else if (last) {
    urlInput.value = last.url;
    setPresetActive(last.id);
  } else if (primary) {
    urlInput.value = primary.url;
    setPresetActive(primary.id);
  }

  loadConfig();
})();
