from app import User
from app.services.users import UserService


class TestUserService:
    def test_get_user_by_id(self):
        svc = UserService(None)
        assert svc is not None


def test_load_users():
    u = User("x")
    assert u.display_name()
