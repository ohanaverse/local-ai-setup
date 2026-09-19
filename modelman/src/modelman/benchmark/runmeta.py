"""Run-provenance helpers shared by the agent and eval benchmark runners."""

import subprocess


def git_sha() -> str:
    """HEAD of the current checkout, or "unknown" when git is unavailable."""
    try:
        result = subprocess.run(
            ["git", "rev-parse", "HEAD"], capture_output=True, text=True, check=False
        )
        return result.stdout.strip() or "unknown"
    except OSError:
        return "unknown"
