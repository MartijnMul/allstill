// SPDX-License-Identifier: AGPL-3.0-only

// The dashboard's only script: signing in and adding a passkey, copying ping
// URLs, and keeping a page current while it is open.

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
