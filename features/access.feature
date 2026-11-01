# Where these scenarios come from
#
# No owner's words describe these behaviours; the checks exist because of defects measured in
# the field. @measured gh api reads of 25 public portable-LLM repositories, 2026-11-01: a chat
# server on 0.0.0.0 with Access-Control-Allow-Origin: * and no client key; a phone model server
# with no key on /v1/chat/completions; an API key documented in a README that no handler read.
# Each of those lets any device on the network, or any web page a visitor opens, drive the model.
#
# Every scenario is bound by name to Go tests, in the comment line above it:
#   # bound to: TestName[, TestName...]
# The binding gate (to be added, as in openwrt-mcp) checks it both ways and runs the tests.

Feature: only a client with its own key reaches a model, and only the models it is given

  Background:
    Given a backend "desktop" serving the model "qwen3:14b"
    And a client "laptop" whose key is K and whose models are "qwen3:14b"

  # bound to: TestRequestWithoutKeyIsRefusedBeforeAnyBackend
  Scenario: a request with no key is refused and never reaches a backend
    When a request for "qwen3:14b" arrives with no Authorization header
    Then the answer is 401
    And the backend receives nothing

  # bound to: TestWrongKeyIsRefused
  Scenario: a wrong key is refused
    When a request arrives with a key that is not K
    Then the answer is 401
    And the backend receives nothing

  # bound to: TestLoopbackRequestStillNeedsAKey
  Scenario: a request from the router itself still needs a key
    When a request with no key arrives on the loopback port
    Then the answer is 401

  # bound to: TestClientReachesOnlyItsModels
  Scenario: a client asks for a model it is not given
    When "laptop" asks for "llama3.3:70b" with key K
    Then the answer is 403
    And the backend receives nothing

  # bound to: TestModelListShowsOnlyTheClientsModels
  Scenario: the model list shows a client only its own models
    When "laptop" asks for /v1/models with key K
    Then the list holds "qwen3:14b" and nothing else

  # bound to: TestHealthAnswersOkAndNothingElse
  Scenario: the health check needs no key and says nothing
    When a request for /health arrives with no key
    Then the answer is 200 with the body "ok"

Feature: a web page cannot drive a model through a visitor's browser

  # bound to: TestNoResponseCarriesAWildcardOrigin
  Scenario: no answer ever allows every origin
    When any request is answered, refused or failed
    Then no Access-Control-Allow-Origin header holds "*"

  # bound to: TestUnlistedOriginIsRefused
  Scenario: a request from a web page the client does not list is refused
    Given "laptop" lists no origin
    When a request with key K arrives with the header Origin "https://example.com"
    Then the answer is 403
    And the backend receives nothing

  # bound to: TestListedOriginIsNamedBack
  Scenario: a listed web page is allowed and named back exactly
    Given "laptop" lists the origin "http://chat.lan:3000"
    When a request with key K arrives from that origin
    Then the answer names "http://chat.lan:3000" in Access-Control-Allow-Origin

  # bound to: TestForeignHostIsRefused
  Scenario: a request for a host name that is not the gateway's is refused
    When a request with key K arrives with the header Host "attacker.example"
    Then the answer is 421
    And the backend receives nothing
