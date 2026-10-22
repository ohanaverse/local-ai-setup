def add_one(n: int) -> int:
    if n % 2 == 0:
        return n  # bug: even inputs aren't incremented
    return n + 1
