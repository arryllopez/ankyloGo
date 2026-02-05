# ankylosaur 🦖 
### **Abuse-Aware API Gateway (Currently only supporting the Gin framework :p)**

`ankylosaur` is a specialized request admission controller designed to distinguish between legitimate users and sophisticated abusers. Unlike traditional rate limiters that rely on static numeric thresholds, `ankylosaur` adapts its enforcement based on endpoint context, historical behavior, and probabilistic risk signals.

---

## 💡 The Problem
Traditional rate limiters treat all traffic as equal. In practice, abuse is contextual:
1. **Static limits** are often too high to catch "low and slow" scraping.
2. **Aggressive limits** cause false positives, frustrating real users during peak traffic.
3. **Context-blindness** ignores the fact that a failed login attempt is more "expensive" than a successful search.

## 🏗️ System Architecture
<img width="420" height="783" alt="image" src="https://github.com/user-attachments/assets/60203047-9745-4727-a2da-6bc551bdd6b9" />


**High-level Architecture Diagram for MVP scope of Ankylosaurus middleware** 


---

## 🎯 Supported Endpoints & Risk Profiles

The gateway applies different logic based on the intent of the request:

| Endpoint | Abuse Risk | Enforcement Strategy |
| :--- | :--- | :--- |
| `POST /login` | **High** | Cost-based limiting; token price increases on auth failures. |
| `GET /search` | **Medium** | Sliding window tracking; detects sustained scraping vs. bursts. |
| `POST /purchase`| **Critical** | Low-latency fraud check; prioritized tokens; strict idempotency. |

---

## 🛡️ Admission Logic

### 1. Rate Limiting Primitives
* **Token Bucket:** Allows for natural bursts in traffic to protect User Experience.
* **Sliding Window:** Tracks sustained rates to identify automated crawlers.
* **Cost Factor:** Not all requests cost "1" token. Risky behavior increases request cost.

### 2. Decision Flow
* Check local cache for immediate "Allow" (Hot-key optimization).
* Fetch policy overrides (e.g., "Is this IP currently in a cooldown?").
* Execute atomic "Check-and-Decrement" on distributed state.
* Emit access log to the async pipeline.

---

## 🧠 Risk Engine & Enforcement
The Risk Engine continuously evaluates the "Health" of an API Key or IP based on:
* **Failure Storms:** Rapid failures on sensitive endpoints.
* **Cardinality:** Unexpectedly high number of different resources accessed by one actor.
* **Churn:** Rapidly changing identifiers (User-Agents, etc.) from a single source.

### Dynamic Actions
| Risk Level | Action Taken |
| :--- | :--- |
| **Moderate** | Reduce bucket burst capacity. |
| **High** | Increase token cost per request. |
| **Critical** | Enforce "Step-up" (require new token/challenge). |
| **Extreme** | Immediate cooldown period. |

---

## ⚡ Reliability & Fail-Safes
* **Fail-Open:** If the distributed state or risk engine is unreachable, the gateway fails open (except for `/purchase`) to prioritize availability.
* **Non-Blocking Observability:** The gateway never waits for the logging pipeline. If the pipeline lags, logs are dropped to protect the request path.
* **Reversibility:** Every enforcement action has a decay period. There are no permanent bans, allowing the system to recover from false positives automatically.

---

## 🚦 Demonstration Tool
The included CLI tool allows you to simulate:
* **Credential Stuffing:** High-frequency login failures.
* **Search Scraping:** Steady, long-term GET requests.
* **Legitimate Bursts:** Short-lived spikes from a single user.

The gateway logs will output the **reasoning** behind every deny (e.g., `DENY: IP_COOLDOWN_ACTIVE` or `DENY: INSUFFICIENT_TOKENS_COST_4`).
