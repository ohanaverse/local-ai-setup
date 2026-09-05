import unittest

from pkg import add_one


class TestAddOneHidden(unittest.TestCase):
    def test_even_input_four(self):
        self.assertEqual(add_one(4), 5)

    def test_even_input_six(self):
        self.assertEqual(add_one(6), 7)


if __name__ == "__main__":
    unittest.main()
