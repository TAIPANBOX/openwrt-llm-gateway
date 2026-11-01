# Where these scenarios come from
#
# No owner's words describe this behaviour. It follows the estate's rule that an audit log never
# holds a secret (openwrt-mcp invariant 3), extended to prompts and completions, which in a home
# are as private as keys.
#
# Every scenario is bound by name to Go tests, in the comment line above it:
#   # bound to: TestName[, TestName...]

Feature: the journal records who used which model, never what was said

  # bound to: TestJournalRecordsTheRequestWithoutContent
  Scenario: an answered request is recorded without its content
    When "laptop" sends a chat request that is answered with usage 120 in and 40 out
    Then the journal holds the time, "laptop", the model, the backend, 200, the latency, 120 and 40
    And the journal holds no part of the prompt, the completion or the key

  # bound to: TestJournalRecordsARefusalWithoutTheKey
  Scenario: a refused request is recorded without the key that was offered
    When a request with a wrong key is refused
    Then the journal holds the time, the refusal and the source address
    And the journal holds no part of the key that was offered

  # bound to: TestStreamingPassesThroughAndIsRecorded
  Scenario: a streamed answer reaches the client as it is produced and is still recorded
    When "laptop" asks for a streamed chat answer
    Then each chunk reaches the client as the backend sends it
    And the journal holds the request once, without its content
