class Base:
    def save(self):
        return True


class User(Base):
    def __init__(self, name):
        self.name = name

    def display_name(self):
        self.save()
        return self.name.title()
