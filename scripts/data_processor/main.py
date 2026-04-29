"""Data Processor — iterative text analysis with configurable depth.

Each iteration performs a progressively deeper pass over the input:
  1: basic stats (word count, char count)
  2: adds unique words and average word length
  3: adds character frequency (top 5)
  4+: adds n-gram analysis with increasing n

The `iterations` param controls how many passes run.
"""

from __future__ import annotations

import json
from collections import Counter

from runner import complete, fail, output, progress, run


def analyze(text: str, depth: int) -> dict[str, object]:
    """Run analysis at the given depth level."""
    words = text.split()
    result: dict[str, object] = {}

    if depth >= 1:
        result["word_count"] = len(words)
        result["char_count"] = len(text)

    if depth >= 2:
        result["unique_words"] = len(set(w.lower() for w in words))
        result["avg_word_length"] = round(sum(len(w) for w in words) / max(len(words), 1), 2)

    if depth >= 3:
        freq = Counter(c for c in text.lower() if c.isalpha())
        result["char_freq_top5"] = dict(freq.most_common(5))

    if depth >= 4:
        n = depth - 1
        ngrams = [" ".join(words[i : i + n]) for i in range(len(words) - n + 1)]
        ngram_freq = Counter(ngrams)
        result[f"{n}-gram_top3"] = dict(ngram_freq.most_common(3))

    return result


def main(params: dict[str, str]) -> None:
    text = params.get("input", "")
    if not text:
        fail("'input' parameter is required")
        return

    try:
        iterations = int(params.get("iterations", "3"))
    except ValueError:
        fail(f"'iterations' must be an integer, got: {params.get('iterations')}")
        return
    output(f"Analyzing text ({len(text)} chars) over {iterations} passes")

    for i in range(1, iterations + 1):
        progress(i, iterations, f"Pass {i} (depth={i})")
        result = analyze(text, depth=i)
        output(f"Pass {i}: {json.dumps(result, default=str)}")

    output(f"Completed {iterations} analysis passes")
    complete()


if __name__ == "__main__":
    run(main)
