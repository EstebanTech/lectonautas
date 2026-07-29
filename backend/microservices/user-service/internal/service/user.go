package service

import (
	"context"
	"log/slog"

	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/EstebanTech/lectonautas/backend/microservices/user-service/internal/domain"
	userv1 "github.com/EstebanTech/lectonautas/backend/microservices/user-service/proto/user/v1"
	"github.com/EstebanTech/lectonautas/backend/shared/logx"
)

func (s *UserService) CreateUser(ctx context.Context, req *userv1.CreateUserRequest) (*userv1.UserResponse, error) {
	email, err := normalizeEmail(req.GetEmail())
	if err != nil {
		return nil, err
	}

	username, err := normalizeUsername(req.GetUsername())
	if err != nil {
		return nil, err
	}

	if err := validatePassword(req.GetPassword()); err != nil {
		return nil, err
	}

	displayName, err := displayNameField.Optional(req.GetDisplayName())
	if err != nil {
		return nil, err
	}
	avatarURL, err := avatarURLField.Optional(req.GetAvatarUrl())
	if err != nil {
		return nil, err
	}
	bio, err := bioField.Optional(req.GetBio())
	if err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.GetPassword()), bcrypt.DefaultCost)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to hash password")
	}

	created, err := s.repo.Create(ctx, &domain.User{
		Email:       email,
		Username:    username,
		Password:    string(hash),
		DisplayName: displayName,
		AvatarURL:   avatarURL,
		Bio:         bio,
	})
	if err != nil {
		return nil, mapRepoErr(ctx, err, "failed to create user")
	}

	// El listado completo quedo viejo: le falta este usuario.
	s.users.Invalidate(ctx)

	return &userv1.UserResponse{User: toProto(created)}, nil
}

// GetUser devuelve el perfil publico. No pide token: el id de un autor viaja en
// cada libro, asi que esto es de acceso publico por diseno, y justo por eso lo
// que devuelve no puede incluir el email ni el estado de la cuenta. Los datos
// propios completos salen por GetCurrentUser.
func (s *UserService) GetUser(ctx context.Context, req *userv1.GetUserRequest) (*userv1.PublicUserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	u, err := s.userByID(ctx, req.GetId())
	if err != nil {
		return nil, err
	}

	return &userv1.PublicUserResponse{User: toPublicProto(u)}, nil
}

// GetAllUsers devuelve todos los usuarios, sin paginacion ni filtros. Tambien
// va por cache-aside: la respuesta completa vive en una sola clave de Valkey.
func (s *UserService) GetAllUsers(ctx context.Context, _ *userv1.GetAllUsersRequest) (*userv1.GetAllUsersResponse, error) {
	var cached []cachedUser
	key, hit := s.users.Get(ctx, &cached, userKeyPart, allUsersKeyPart)

	users := fromCachedList(cached)
	if !hit {
		var err error
		if users, err = s.repo.GetAll(ctx); err != nil {
			return nil, status.Error(codes.Internal, "failed to get users")
		}
		s.users.Set(ctx, key, toCachedList(users))
	}

	// Perfiles publicos: este listado tampoco pide token.
	out := toPublicProtos(users)

	return &userv1.GetAllUsersResponse{Users: out, Total: int32(len(out))}, nil
}

func (s *UserService) UpdateUser(ctx context.Context, req *userv1.UpdateUserRequest) (*userv1.UserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	// Una cuenta solo la edita su dueno. El id sigue en la ruta por comodidad
	// del cliente, pero quien manda es el token: sin este chequeo bastaba con
	// conocer un id ajeno (y los ids de autor son publicos, van en cada libro)
	// para reescribir el perfil de cualquiera.
	if err := s.requireSelf(ctx, req.GetId()); err != nil {
		return nil, err
	}

	upd, err := buildUserUpdate(req)
	if err != nil {
		return nil, err
	}

	updated, err := s.repo.Update(ctx, upd)
	if err != nil {
		return nil, mapRepoErr(ctx, err, "failed to update user")
	}

	s.users.Invalidate(ctx)

	return &userv1.UserResponse{User: toProto(updated)}, nil
}

// buildUserUpdate traduce la peticion a la modificacion parcial del dominio, y
// exige que traiga al menos un campo: un update sin nada que cambiar es un error
// del cliente, no una escritura vacia que se acepta en silencio.
func buildUserUpdate(req *userv1.UpdateUserRequest) (*domain.UserUpdate, error) {
	upd := &domain.UserUpdate{ID: req.GetId()}

	if req.Username != nil {
		username, err := normalizeUsername(req.GetUsername())
		if err != nil {
			return nil, err
		}
		upd.Username = &username
	}

	var err error
	if upd.DisplayName, err = displayNameField.Update(req.DisplayName); err != nil {
		return nil, err
	}
	if upd.AvatarURL, err = avatarURLField.Update(req.AvatarUrl); err != nil {
		return nil, err
	}
	if upd.Bio, err = bioField.Update(req.Bio); err != nil {
		return nil, err
	}
	if req.IsActive != nil {
		v := req.GetIsActive()
		upd.IsActive = &v
	}

	if upd.Username == nil && upd.DisplayName == nil && upd.AvatarURL == nil &&
		upd.Bio == nil && upd.IsActive == nil {
		return nil, status.Error(codes.InvalidArgument, "no fields to update")
	}
	return upd, nil
}

// DeleteUser da de baja la cuenta del llamante y, con ella, todo lo que colgaba
// del usuario: sus sesiones (CASCADE en esta misma base) y su contenido en
// library-service, que se borra antes por gRPC.
//
// El contenido va primero a proposito. Si fallara, la cuenta sigue en pie y el
// cliente puede reintentar; al reves quedaria obra sin dueno y sin forma de
// llegar a ella, porque el author_id apunta a un usuario que ya no existe.
func (s *UserService) DeleteUser(ctx context.Context, req *userv1.DeleteUserRequest) (*userv1.DeleteUserResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	// Solo el dueno puede darse de baja: es la operacion mas destructiva de la
	// API y con la cascada se lleva por delante toda su obra.
	if err := s.requireSelf(ctx, req.GetId()); err != nil {
		return nil, err
	}

	// Los hashes se leen ANTES de borrar: el CASCADE se lleva las filas de
	// session y despues no habria de donde sacarlos. Hacen falta para tirar las
	// entradas que esas sesiones tengan en Valkey — sin eso el token de una
	// cuenta ya borrada sigue resolviendose desde el cache, porque resolveSession
	// lo consulta antes que la BD, y con el se pueden seguir creando libros
	// durante toda la ventana de sessionCacheTTL.
	//
	// Un fallo aqui no aborta la baja: es preferible una cuenta borrada con
	// sesiones que sobreviven hasta que vence su TTL que una cuenta que no se
	// puede borrar.
	staleSessions, err := s.sessions.TokenHashesByUser(ctx, req.GetId())
	if err != nil {
		logx.From(ctx).Error("list user sessions failed", slog.String("error", err.Error()))
	}

	// Los dos vecinos van ANTES de borrar la fila del usuario, y un fallo
	// aborta la baja. Es la unica forma de que un reintento pueda arreglarlo:
	// si se borrara primero el usuario, su id se perderia y ya no habria con
	// que pedir la limpieza de lo que dejo en las otras bases.
	if err := s.content.DeleteAuthorContent(ctx, req.GetId()); err != nil {
		logx.From(ctx).Error("delete author content failed", slog.String("error", err.Error()))
		return nil, status.Error(codes.Internal, "failed to delete the account content")
	}

	if err := s.interactions.DeleteUserInteractions(ctx, req.GetId()); err != nil {
		logx.From(ctx).Error("delete user interactions failed", slog.String("error", err.Error()))
		return nil, status.Error(codes.Internal, "failed to delete the account content")
	}

	if err := s.repo.Delete(ctx, req.GetId()); err != nil {
		return nil, mapRepoErr(ctx, err, "failed to delete user")
	}

	// La cuenta ya no existe: sus tokens no pueden seguir abriendo nada. Esto
	// se borra por clave y no por version, porque es revocacion y no frescura:
	// tiene que caer esta sesion, no el cache entero.
	for _, hash := range staleSessions {
		if err := s.cache.Delete(ctx, hash); err != nil {
			logx.From(ctx).Error("session cache delete failed", slog.String("error", err.Error()))
		}
	}

	// Una sola invalidacion se lleva por delante tanto la entrada del usuario
	// como el listado completo, que tambien quedo viejo: es la ventaja del
	// contador de version frente a acordarse de cada clave afectada.
	s.users.Invalidate(ctx)

	return &userv1.DeleteUserResponse{Success: true}, nil
}
