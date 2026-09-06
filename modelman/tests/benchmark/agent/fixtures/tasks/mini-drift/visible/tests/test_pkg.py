import unittest

from pkg import add_one


class TestAddOne(unittest.TestCase):
    def test_odd_input(self):
        self.assertEqual(add_one(3), 4)


if __name__ == "__main__":
    unittest.main()
