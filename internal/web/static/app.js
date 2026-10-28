// SPDX-License-Identifier: AGPL-3.0-only

// The dashboard's only script: signing in and adding a passkey, copying ping
// URLs, the tips on the last runs, and keeping a page current while it is
// open.

const b64 = {
  encode(buffer) {
    let s = "";
    for (const byte of new Uint8Array(buffer)) s += String.fromCharCode(byte);
    return btoa(s).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
  },
  decode(text) {
    const s = atob(text.replaceAll("-", "+").replaceAll("_", "/"));
    return Uint8Array.from(s, (c) => c.charCodeAt(0));
  },
};

async function post(url, body) {
  const response = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body ?? {}),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || "That did not work. Try again.");
  return data;
}

// The server's options carry binary fields as base64url; the browser wants
// bytes, and gives bytes back.
function creationOptions({ publicKey: o }) {
  o.challenge = b64.decode(o.challenge);
  o.user.id = b64.decode(o.user.id);
  for (const c of o.excludeCredentials ?? []) c.id = b64.decode(c.id);
  return { publicKey: o };
}

function requestOptions({ publicKey: o }) {
  o.challenge = b64.decode(o.challenge);
  for (const c of o.allowCredentials ?? []) c.id = b64.decode(c.id);
  return { publicKey: o };
}

function credentialJSON(c) {
  const r = c.response;
  const out = {
    id: c.id,
    rawId: b64.encode(c.rawId),
    type: c.type,
    authenticatorAttachment: c.authenticatorAttachment ?? undefined,
    clientExtensionResults: c.getClientExtensionResults(),
    response: { clientDataJSON: b64.encode(r.clientDataJSON) },
  };
  if (r.attestationObject) {
    out.response.attestationObject = b64.encode(r.attestationObject);
    out.response.transports = r.getTransports?.() ?? [];
  } else {
    out.response.authenticatorData = b64.encode(r.authenticatorData);
    out.response.signature = b64.encode(r.signature);
    if (r.userHandle) out.response.userHandle = b64.encode(r.userHandle);
  }
  return out;
}

// A cancelled or timed-out passkey prompt is not an error worth a sentence.
function message(err) {
  if (err.name === "NotAllowedError") return "The passkey prompt was closed. Try again when you are ready.";
  if (err.name === "InvalidStateError") return "This device has a passkey for this site already.";
  return err.message;
}

function busy(form, on) {
  form.querySelector("button[type=submit]").disabled = on;
}

const login = document.getElementById("login");
if (login) {
  const alert = login.querySelector("[role=alert]");
  login.addEventListener("submit", async (event) => {
    event.preventDefault();
    alert.textContent = "";
    busy(login, true);
    try {
      const options = await post("/login/begin");
      const credential = await navigator.credentials.get(requestOptions(options));
      const done = await post("/login/finish", credentialJSON(credential));
      location.assign(done.location);
    } catch (err) {
      alert.textContent = message(err);
      busy(login, false);
    }
  });
}

const setup = document.getElementById("setup");
if (setup) {
  const token = location.hash.slice(1);
  const failed = document.getElementById("setup-error");
  const showError = (text) => {
    setup.hidden = true;
    failed.hidden = false;
    failed.querySelector("[role=alert]").textContent = text;
  };
  // Keep the token out of the address bar and the history.
  history.replaceState(null, "", location.pathname);
  if (!token) {
    showError("This link is incomplete. Copy the whole link and try again.");
  } else {
    post("/setup/link", { token })
      .then((link) => {
        setup.querySelector("[data-name]").textContent = link.name;
        if (link.signed_in_as) {
          setup.querySelector("[data-other-name]").textContent = link.signed_in_as;
          setup.querySelector("[data-other]").hidden = false;
        }
        setup.hidden = false;
        setup.querySelector("input[name=name]").focus();
      })
      .catch((err) => showError(err.message));
  }
  const alert = setup.querySelector("[role=alert]");
  setup.addEventListener("submit", async (event) => {
    event.preventDefault();
    alert.textContent = "";
    busy(setup, true);
    const name = setup.elements.name.value.trim();
    try {
      const options = await post("/setup/begin", { token, name });
      const credential = await navigator.credentials.create(creationOptions(options));
      const done = await post("/setup/finish", { token, name, credential: credentialJSON(credential) });
      location.assign(done.location);
    } catch (err) {
      alert.textContent = message(err);
      busy(setup, false);
    }
  });
}

// Copy buttons may be replaced when the page refreshes, so one listener
// serves them all.
document.addEventListener("click", async (event) => {
  const button = event.target.closest("[data-copy]");
  if (!button) return;
  const text = document.getElementById(button.dataset.copy).textContent;
  try {
    await navigator.clipboard.writeText(text);
    button.textContent = "Copied";
  } catch {
    button.textContent = "Copy failed";
  }
  setTimeout(() => (button.textContent = "Copy"), 2000);
});

// A bar in the last runs says what it was and when. With a mouse the tip
// comes after a short pause, so moving across the page sets nothing off, and
// then follows the pointer from bar to bar; on a phone a tap shows it. The
// bars are narrow, so the one nearest the pointer counts.
const tip = document.createElement("div");
tip.className = "tip";
tip.setAttribute("role", "tooltip");
tip.hidden = true;
document.body.append(tip);
let tipBar = null;
let tipWait = 0;

function barAt(bars, x) {
  let nearest = null;
  let distance = Infinity;
  for (const bar of bars.querySelectorAll("[data-tip]")) {
    const r = bar.getBoundingClientRect();
    const d = Math.max(r.left - x, x - r.right, 0);
    if (d < distance) [nearest, distance] = [bar, d];
  }
  return nearest;
}

function showTip(bar) {
  if (!bar || bar === tipBar) return;
  tipBar?.classList.remove("on");
  tipBar = bar;
  bar.classList.add("on");
  tip.textContent = bar.dataset.tip;
  tip.hidden = false;
  const r = bar.getBoundingClientRect();
  const left = Math.min(Math.max(8, r.left + r.width / 2 - tip.offsetWidth / 2), innerWidth - tip.offsetWidth - 8);
  const above = r.top - tip.offsetHeight - 6;
  tip.style.left = `${left}px`;
  tip.style.top = `${above < 8 ? r.bottom + 6 : above}px`;
}

function hideTip() {
  clearTimeout(tipWait);
  tipBar?.classList.remove("on");
  tipBar = null;
  tip.hidden = true;
}

document.addEventListener("pointermove", (event) => {
  if (event.pointerType !== "mouse") return;
  const bars = event.target.closest?.(".bars");
  if (!bars) {
    hideTip();
    return;
  }
  if (tipBar) {
    showTip(barAt(bars, event.clientX));
    return;
  }
  clearTimeout(tipWait);
  tipWait = setTimeout(() => showTip(barAt(bars, event.clientX)), 300);
});

document.addEventListener("click", (event) => {
  const bars = event.target.closest(".bars");
  if (!bars) {
    hideTip();
    return;
  }
  // A second tap closes it; a mouse just leaves.
  const bar = barAt(bars, event.clientX);
  if (bar === tipBar && ["touch", "pen"].includes(event.pointerType)) hideTip();
  else showTip(bar);
});

addEventListener("scroll", hideTip, { capture: true, passive: true });
document.addEventListener("pointerout", (event) => {
  if (event.pointerType === "mouse" && !event.relatedTarget) hideTip();
});

// Until the live stream comes, a page with live content fetches itself again
// every 30 seconds while it is in view. It leaves the page alone while
// someone works in it (focus or a selection inside), and says so when it
// cannot refresh: a monitoring page must never look current when it is not.
const live = document.getElementById("live");
const stale = document.getElementById("stale");
if (live && stale) {
  let updated = new Date();
  const inUse = () => {
    const selection = getSelection();
    return live.contains(document.activeElement) ||
      (selection && !selection.isCollapsed && live.contains(selection.anchorNode));
  };
  setInterval(async () => {
    if (document.visibilityState !== "visible" || inUse()) return;
    try {
      const response = await fetch(location.href, { headers: { Accept: "text/html" } });
      if (response.redirected && new URL(response.url).pathname === "/login") {
        location.assign("/login");
        return;
      }
      if (!response.ok) throw new Error(response.statusText);
      const next = new DOMParser().parseFromString(await response.text(), "text/html").getElementById("live");
      if (!next) throw new Error("no live content");
      if (inUse()) return;
      const log = live.querySelector("pre.log");
      const scrolled = log ? log.scrollTop : 0;
      live.replaceChildren(...next.childNodes);
      hideTip();
      const newLog = live.querySelector("pre.log");
      if (newLog) newLog.scrollTop = scrolled;
      updated = new Date();
      stale.textContent = "";
    } catch {
      const at = updated.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
      stale.textContent = `Not updated since ${at}: Allstill cannot be reached. This page shows how things were then.`;
    }
  }, 30_000);
}

// Web Push (ADR 0003). The page that adds a device subscribes this browser
// for the server's key; every signed-in page sends the subscription again, so
// a changed one is picked up, and one made for another key is made anew.
const pushKey = document.querySelector("meta[name=allstill-push-key]")?.content;
const pushable = pushKey && "serviceWorker" in navigator && "PushManager" in window;

function deviceName() {
  const ua = navigator.userAgent;
  const device = /iPhone/.test(ua) ? "iPhone" : /iPad/.test(ua) ? "iPad" : /Android/.test(ua) ? "Android"
    : /Mac/.test(ua) ? "Mac" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "Device";
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome"
    : /Safari\//.test(ua) ? "Safari" : "";
  return browser ? `${device}, ${browser}` : device;
}

// The key a subscription was made for; a browser that does not tell is
// taken at its word that it is the current one.
function keyOf(subscription) {
  const key = subscription.options?.applicationServerKey;
  return key ? b64.encode(key) : pushKey;
}

async function subscribe(registration) {
  return registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64.decode(pushKey) });
}

async function sendSubscription(subscription) {
  return post("/channels/push", { name: deviceName(), key: keyOf(subscription), ...subscription.toJSON() });
}

// The channel of this browser, once the server has seen its subscription.
async function renew() {
  if (!pushable || Notification.permission !== "granted") return null;
  const registration = await navigator.serviceWorker.getRegistration("/");
  let subscription = await registration?.pushManager.getSubscription();
  if (!subscription) return null;
  if (keyOf(subscription) !== pushKey) {
    await subscription.unsubscribe();
    subscription = await subscribe(registration);
  }
  return sendSubscription(subscription);
}

let thisDeviceID = null;
let renewing = true;
const thisDevice = renew()
  .then((channel) => {
    thisDeviceID = channel?.id ?? null;
    return channel;
  })
  .catch(() => null)
  .finally(() => (renewing = false));

const push = document.getElementById("push");
if (push) {
  const add = push.querySelector("[data-push-add]");
  const state = push.querySelector("[data-push-state]");
  const alert = push.querySelector("[role=alert]");
  const standalone = matchMedia("(display-mode: standalone)").matches || navigator.standalone;
  if (!pushable) {
    if (/iPhone|iPad/.test(navigator.userAgent) && !standalone) push.querySelector("[data-push-ios]").hidden = false;
    else state.textContent = "This browser cannot get push notifications.";
  } else {
    thisDevice.then((channel) => {
      if (channel) {
        const row = document.querySelector(`[data-channel="${CSS.escape(channel.id)}"]`);
        row?.querySelector("[data-this-device]")?.removeAttribute("hidden");
        state.textContent = channel.confirmed
          ? "This device gets alerts."
          : "This device is added. Send it a test and open the notification to confirm it.";
        if (!row && channel.id) location.reload();
      } else if (Notification.permission === "denied") {
        state.textContent = "Notifications are blocked for Allstill in this browser's settings.";
      } else {
        add.hidden = false;
      }
    });
    add.addEventListener("click", async () => {
      alert.textContent = "";
      add.disabled = true;
      try {
        if ((await Notification.requestPermission()) !== "granted") {
          throw new Error("Notifications were not allowed. Allow them for Allstill and try again.");
        }
        const registration = await navigator.serviceWorker.register("/sw.js", { scope: "/" });
        await navigator.serviceWorker.ready;
        let subscription = await registration.pushManager.getSubscription();
        if (subscription && keyOf(subscription) !== pushKey) {
          await subscription.unsubscribe();
          subscription = null;
        }
        subscription ??= await subscribe(registration);
        await sendSubscription(subscription);
        location.reload();
      } catch (err) {
        alert.textContent = err.message;
        add.disabled = false;
      }
    });
  }

  // Removing this device also unsubscribes this browser, or the next page
  // would add it again. Until the page knows which device it is on, it waits.
  document.addEventListener("submit", async (event) => {
    const form = event.target.closest("[data-remove]");
    if (!form || (!renewing && thisDeviceID !== form.dataset.remove)) return;
    event.preventDefault();
    if ((await thisDevice)?.id !== form.dataset.remove) {
      form.submit();
      return;
    }
    const registration = await navigator.serviceWorker.getRegistration("/");
    await (await registration?.pushManager.getSubscription())?.unsubscribe().catch(() => {});
    form.submit();
  });
}
