"""Error Stages — partial completion then deliberate failure.

Exercises the error propagation path: output + progress for N steps,
then fail() with an explicit message and traceback at step fail_at.
"""

from __future__ import annotations

from runner import complete, fail, output, progress, run


def main(params: dict[str, str]) -> None:
    try:
        fail_at = int(params.get("fail_at", "3"))
        total = int(params.get("total", "5"))
    except ValueError:
        fail("'fail_at' and 'total' must be integers")
        return

    for step in range(1, total + 1):
        if step == fail_at:
            output(f"Step {step}/{total}: FAILING HERE")
            fail(
                f"Intentional failure at step {step}",
                f"error_stages.main: step={step}, total={total}",
            )
            return

        output(f"Step {step}/{total}: OK")
        progress(step, total, f"Step {step}")

    complete()


if __name__ == "__main__":
    run(main)
