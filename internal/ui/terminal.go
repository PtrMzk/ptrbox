package ui

import "os"

// Terminal reports whether f is a terminal that will render the escape
// sequences this package writes - and, on a platform where rendering them is
// something a process has to ask for, asks.
//
// It is the capability half of the colour decision. Whether the user WANTS
// colour (--no-color, NO_COLOR) is main's half, and is asked first: a console
// nobody is going to colour is left in the mode it was found in.
func Terminal(f *os.File) bool { return terminal(f) }
