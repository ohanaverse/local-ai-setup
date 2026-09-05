"""Collection config for the agent-benchmark tests.

fixtures/tasks/ holds *task bundles* — miniature repositories that are data
for these tests, not tests. Their files are named test_pkg.py /
test_hidden.py on purpose (that is the layout unittest discovery expects
inside a seeded workspace, and the same layout as the real
benchmarks/tasks/day31-drift bundle), which is exactly the name pytest
collects. Collected in place they import `pkg`, a module that only exists
inside a seeded temp workspace, so the entire suite fails at collection
rather than just these fixtures.
"""

collect_ignore = ["fixtures"]
